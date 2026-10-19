package metadata

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// DetailService reads display evidence without creating media, demand or jobs.
type DetailService struct {
	provider ports.MetadataDetailBrowser
}

func NewDetailService(provider ports.MetadataDetailBrowser) *DetailService {
	return &DetailService{provider: provider}
}

func (s *DetailService) Credits(ctx context.Context, actor ports.Actor, mediaType string, tmdbID int64) (*ports.MetadataCredits, error) {
	if !canReadDetails(actor) {
		return nil, ports.ErrForbidden
	}
	if tmdbID <= 0 || (mediaType != "movie" && mediaType != "tv") {
		return nil, ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.provider == nil {
		return nil, ports.ErrUnavailable
	}
	// Keep the provider's physical request ownership until synchronous I/O returns.
	result, err := s.provider.GetMediaCredits(ctx, mediaType, tmdbID, "zh-CN")
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, detailProviderError(err)
	}
	if result == nil || result.TMDBID != tmdbID || result.MediaType != mediaType || result.Cast == nil || len(result.Cast) > 24 {
		return nil, ports.ErrBadResponse
	}
	out := *result
	out.Cast = slices.Clone(result.Cast)
	for _, member := range out.Cast {
		if member.TMDBID <= 0 || strings.TrimSpace(member.Name) == "" ||
			!safeDetailText(member.Name, false) || !safeDetailText(member.Character, false) ||
			!detailImageURL(member.ProfileURL, "w185") {
			return nil, ports.ErrBadResponse
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *DetailService) Season(ctx context.Context, actor ports.Actor, tmdbID int64, season int) (*ports.MetadataSeasonDetails, error) {
	if !canReadDetails(actor) {
		return nil, ports.ErrForbidden
	}
	if tmdbID <= 0 || season < 0 {
		return nil, ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.provider == nil {
		return nil, ports.ErrUnavailable
	}
	result, err := s.provider.GetTVSeasonDetails(ctx, tmdbID, season, "zh-CN")
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	if err != nil {
		return nil, detailProviderError(err)
	}
	if result == nil || result.TMDBID != tmdbID || result.SeasonNumber != season || !result.Complete || result.Episodes == nil {
		return nil, ports.ErrBadResponse
	}
	out := *result
	out.Episodes = slices.Clone(result.Episodes)
	ids := make(map[int64]bool, len(out.Episodes))
	numbers := make(map[int]bool, len(out.Episodes))
	for _, episode := range out.Episodes {
		if episode.TMDBID <= 0 || episode.EpisodeNumber <= 0 || ids[episode.TMDBID] || numbers[episode.EpisodeNumber] ||
			!safeDetailText(episode.Name, false) || !safeDetailText(episode.Overview, true) ||
			!detailDate(episode.AirDate) || !detailImageURL(episode.StillURL, "w300") {
			return nil, ports.ErrBadResponse
		}
		ids[episode.TMDBID], numbers[episode.EpisodeNumber] = true, true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &out, nil
}

func canReadDetails(actor ports.Actor) bool {
	// Authentication middleware resolves current account activity before this read.
	id, err := uuid.Parse(actor.ID)
	return err == nil && id != uuid.Nil && actor.Type == ports.ActorTypeUser &&
		(actor.Role == ports.RoleMember || actor.Role == ports.RoleAdmin) &&
		(actor.Scopes == nil || slices.Contains(actor.Scopes, "read"))
}

func safeDetailText(text string, multiline bool) bool {
	if !utf8.ValidString(text) || media.UnsafeCandidateTitle(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && (!multiline || (r != '\n' && r != '\r' && r != '\t')) {
			return false
		}
	}
	return true
}

func detailDate(date string) bool {
	if date == "" {
		return true
	}
	parsed, err := time.Parse(time.DateOnly, date)
	return err == nil && parsed.Year() >= 1800 && parsed.Format(time.DateOnly) == date
}

func detailImageURL(raw, size string) bool {
	if raw == "" {
		return true
	}
	prefix := "https://image.tmdb.org/t/p/" + size
	if strings.HasPrefix(raw, "https://image.tmdb.org/t/p/w500/") {
		prefix = "https://image.tmdb.org/t/p/w500"
	}
	if !strings.HasPrefix(raw, prefix+"/") {
		return false
	}
	path := strings.TrimPrefix(raw, prefix)
	// Reuse the domain's opaque path validation for fixed profile/still projections.
	return media.TMDBPosterPath("https://image.tmdb.org/t/p/w342"+path) == path
}

func detailProviderError(err error) error {
	for _, class := range []error{ports.ErrNotFound, ports.ErrUnavailable, ports.ErrBadResponse, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, class) {
			return class
		}
	}
	if limited, ok := ports.IsRateLimited(err); ok {
		return ports.NewRateLimitError(limited.RetryAfter, "")
	}
	return ports.ErrUnavailable
}
