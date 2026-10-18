package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var (
	ErrLowConfidence   = errors.New("recognition confidence too low, manual review required")
	ErrNoMetadataMatch = errors.New("no metadata found for recognized movie")
)

type MatchResult struct {
	Media          *ports.LinkedMovie
	Unit           *ports.LinkedMovieUnit
	Metadata       *ports.MovieMetadata
	Recognition    domainMedia.Recognition
	RequiresReview bool
}
type Matcher struct {
	repository ports.MovieLinkRepository
	provider   ports.MovieIdentityProvider
}

func NewMatcher(repository ports.MovieLinkRepository, provider ports.MovieIdentityProvider) *Matcher {
	return &Matcher{repository: repository, provider: provider}
}

// MatchAndLinkFile freezes the registered source before metadata I/O and binds it atomically.
func (m *Matcher) MatchAndLinkFile(ctx context.Context, rootID uuid.UUID, relPath string) (*MatchResult, error) {
	rec := domainMedia.Recognize(relPath)
	res := &MatchResult{Recognition: rec, RequiresReview: rec.RequiresReview}
	if rec.RequiresReview || rec.Confidence < 0.7 {
		return res, ErrLowConfidence
	}
	var explicitTMDB int64
	if rec.Identity != nil && rec.Identity.Namespace == "tmdb.movie" {
		var err error
		explicitTMDB, err = strconv.ParseInt(rec.Identity.ExternalID, 10, 64)
		if err != nil || explicitTMDB <= 0 {
			res.RequiresReview = true
			return res, ports.ErrRejected
		}
	}
	source, err := m.repository.LoadSource(ctx, rootID, relPath)
	if err != nil {
		res.RequiresReview = true
		return res, err
	}
	var meta *ports.MovieMetadata
	if rec.Identity != nil {
		if rec.Identity.Namespace == "tmdb.movie" {
			meta, err = m.provider.GetMovieDetails(ctx, explicitTMDB, "zh-CN")
		} else {
			meta, err = m.provider.FindMovieByExternalID(ctx, rec.Identity.Namespace, rec.Identity.ExternalID)
		}
	} else {
		var search ports.MovieIdentitySearch
		search, err = m.provider.SearchMovieIdentityCandidates(ctx, rec.Title, rec.Year)
		if err == nil {
			candidates := make([]domainMedia.MovieSearchIdentity, len(search.Candidates))
			for i, c := range search.Candidates {
				if c != nil {
					candidates[i] = domainMedia.MovieSearchIdentity{TMDBID: c.TMDBID, Title: c.Title, OriginalTitle: c.OriginalTitle, Year: c.Year, MediaType: c.MediaType}
				}
			}
			if index, ok := domainMedia.SelectMovieSearchIdentity(rec, candidates, search.Complete); ok {
				meta = search.Candidates[index]
			}
		}
	}
	if err != nil || meta == nil {
		res.RequiresReview = true
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrNoMetadataMatch, err)
		}
		return res, ErrNoMetadataMatch
	}
	if meta.TMDBID <= 0 || strings.TrimSpace(meta.Title) == "" || (meta.MediaType != "" && meta.MediaType != "movie") || (explicitTMDB > 0 && meta.TMDBID != explicitTMDB) || (rec.Identity != nil && rec.Identity.Namespace == "imdb" && !strings.EqualFold(meta.IMDbID, rec.Identity.ExternalID)) {
		res.RequiresReview = true
		return res, ports.ErrBadResponse
	}
	res.Metadata = meta
	quality, err := json.Marshal(rec.Quality)
	if err != nil {
		res.RequiresReview = true
		return res, err
	}
	linked, err := m.repository.Link(ctx, ports.MovieLinkInput{Source: source, Metadata: *meta, QualityJSON: quality})
	if err != nil {
		res.RequiresReview = true
		return res, err
	}
	res.Media = &linked.Media
	res.Unit = &linked.Unit
	return res, nil
}
