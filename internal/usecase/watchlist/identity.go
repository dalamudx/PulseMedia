// Package watchlist prepares public wish-list observations for authorized subscription creation.
package watchlist

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/text/unicode/norm"
)

type IdentityState string

const (
	IdentityUnresolved IdentityState = "unresolved"
	IdentityIncomplete IdentityState = "incomplete"
	IdentityAmbiguous  IdentityState = "ambiguous"
	IdentityMatched    IdentityState = "matched"
)

// Identity is evidence for a later fenced transaction, not permission to create.
// TMDBID remains zero unless all aliases were searched completely and a fresh
// typed detail agreed with the sole matching identity.
type Identity struct {
	State     IdentityState
	TMDBID    int64
	MediaType string
}

type IdentityResolver struct {
	metadata ports.WatchlistIdentityMetadata
}

func NewIdentityResolver(metadata ports.WatchlistIdentityMetadata) *IdentityResolver {
	return &IdentityResolver{metadata: metadata}
}

func (r *IdentityResolver) Resolve(ctx context.Context, item ports.DoubanWishItem) (Identity, error) {
	out := Identity{State: IdentityUnresolved, MediaType: item.MediaType}
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	id, err := strconv.ParseUint(item.DoubanID, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != item.DoubanID || (item.MediaType != "movie" && item.MediaType != "tv") || (item.Year != 0 && (item.Year < 1800 || item.Year > 9999)) || len(item.Titles) == 0 || len(item.Titles) > 8 {
		return Identity{}, ports.ErrRejected
	}
	queries := make([]string, 0, len(item.Titles))
	aliases := make(map[string]bool, len(item.Titles))
	for _, title := range item.Titles {
		key := identityTitle(title)
		if key == "" {
			return Identity{}, ports.ErrRejected
		}
		if !aliases[key] {
			aliases[key] = true
			queries = append(queries, strings.Join(strings.Fields(title), " "))
		}
	}
	if item.Year == 0 {
		return out, nil
	}
	if r == nil || r.metadata == nil {
		return Identity{}, ports.ErrUnavailable
	}
	matches := make(map[int64]bool)
	incomplete := false
	for _, query := range queries {
		if err = ctx.Err(); err != nil {
			return Identity{}, err
		}
		result, lookupErr := r.metadata.SearchMediaIdentityCandidates(ctx, query, item.MediaType, item.Year)
		if err = ctx.Err(); err != nil {
			return Identity{}, err
		}
		if lookupErr != nil {
			return Identity{}, identityError(lookupErr)
		}
		if !result.Complete {
			incomplete = true
		}
		if len(result.Candidates) > 100 {
			return Identity{}, ports.ErrBadResponse
		}
		for _, candidate := range result.Candidates {
			if candidate == nil || candidate.TMDBID <= 0 || candidate.MediaType != item.MediaType || candidate.Year < 0 || candidate.Year > 9999 || identityTitle(candidate.Title) == "" || (candidate.OriginalTitle != "" && identityTitle(candidate.OriginalTitle) == "") {
				return Identity{}, ports.ErrBadResponse
			}
			if !aliases[identityTitle(candidate.Title)] && !aliases[identityTitle(candidate.OriginalTitle)] {
				continue
			}
			switch candidate.Year {
			case 0:
				incomplete = true
			case item.Year:
				matches[candidate.TMDBID] = true
			}
		}
	}
	if len(matches) > 1 {
		out.State = IdentityAmbiguous
		return out, nil
	}
	if incomplete {
		out.State = IdentityIncomplete
		return out, nil
	}
	if len(matches) == 0 {
		return out, nil
	}
	var matchedID int64
	for candidateID := range matches {
		matchedID = candidateID
	}
	var detail *ports.MovieMetadata
	if item.MediaType == "movie" {
		detail, err = r.metadata.GetMovieDetails(ctx, matchedID, "zh-CN")
	} else {
		var tv *ports.TVDetails
		tv, err = r.metadata.GetTVDetails(ctx, matchedID, "zh-CN")
		if tv != nil {
			detail = &ports.MovieMetadata{TMDBID: tv.TMDBID, MediaType: "tv", Title: tv.Title, OriginalTitle: tv.OriginalTitle, Year: tv.Year}
		}
	}
	if ctx.Err() != nil {
		return Identity{}, ctx.Err()
	}
	if errors.Is(err, ports.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return Identity{}, identityError(err)
	}
	if detail == nil {
		return Identity{}, ports.ErrBadResponse
	}
	if detail.TMDBID != matchedID || detail.MediaType != item.MediaType || detail.Year != item.Year || (!aliases[identityTitle(detail.Title)] && !aliases[identityTitle(detail.OriginalTitle)]) {
		return out, nil
	}
	out.State, out.TMDBID = IdentityMatched, matchedID
	return out, nil
}

func identityTitle(title string) string {
	if !utf8.ValidString(title) || len(title) > 512 {
		return ""
	}
	for _, r := range title {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return ""
		}
	}
	// Punctuation and symbols distinguish titles; only Unicode composition,
	// case and whitespace are normalized for automatic identity selection.
	return strings.ToLower(norm.NFC.String(strings.Join(strings.Fields(title), " ")))
}

func identityError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrTimeout, ports.ErrAuthRequired, ports.ErrBlocked, ports.ErrBadResponse, ports.ErrNotFound} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if rate, ok := ports.IsRateLimited(err); ok {
		delay := rate.RetryAfter
		if delay < time.Second {
			delay = time.Second
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		return ports.NewRateLimitError(delay, "metadata rate limited")
	}
	return ports.ErrUnavailable
}
