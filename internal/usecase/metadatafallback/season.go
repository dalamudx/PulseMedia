package metadatafallback

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Service reads optional source observations. It does not create units, retire
// omitted episodes, or authorize acquisition; the refresh UoW owns those decisions.
type Service struct {
	identity ports.TVArtworkIdentityResolver
	source   ports.TVDBProvider
	resolver ports.TVDBEpisodeResolver
}

func New(identity ports.TVArtworkIdentityResolver, source ports.TVDBProvider, resolver ports.TVDBEpisodeResolver) *Service {
	return &Service{identity: identity, source: source, resolver: resolver}
}
func (s *Service) ReadSeasonSupplement(ctx context.Context, tmdbID int64, season int, primary *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tmdbID <= 0 || season < 0 || season > 999 {
		return nil, ports.ErrRejected
	}
	if s.identity == nil || s.source == nil || s.resolver == nil {
		return nil, ports.ErrUnavailable
	}
	if primary != nil && (primary.SeasonNumber != season || !primary.Complete) {
		return nil, ports.ErrBadResponse
	}
	output := &ports.TVDBSeasonSupplement{TMDBSeriesID: tmdbID, Metadata: ports.TVSeasonMetadata{SeasonNumber: season, Complete: false, Episodes: []ports.TVEpisodeMetadata{}}, Added: []ports.TVDBEpisodeResolution{}, UnmatchedTVDBIDs: []int64{}}
	baseIDs := []tvdb.EpisodeIdentity{}
	if primary != nil {
		for _, ep := range primary.Episodes {
			copied, err := copyEpisode(ep)
			if err != nil {
				return nil, err
			}
			baseIDs = append(baseIDs, tvdb.EpisodeIdentity{ID: ep.TMDBEpisodeID, Season: season, Number: ep.EpisodeNumber})
			output.Metadata.Episodes = append(output.Metadata.Episodes, copied)
		}
	}
	if _, err := tvdb.SelectAdditions(season, baseIDs, nil); err != nil {
		return nil, ports.ErrBadResponse
	}
	bounded, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	identity, err := s.identity.ResolveTVArtworkIdentity(bounded, tmdbID)
	if bounded.Err() != nil {
		return nil, bounded.Err()
	}
	if err != nil {
		return nil, providerError(err)
	}
	if identity.TMDBID != tmdbID || identity.TVDBID <= 0 {
		return nil, ports.ErrBadResponse
	}
	output.TVDBSeriesID = identity.TVDBID
	source, err := s.source.GetSeasonMetadata(bounded, identity.TVDBID, season)
	if bounded.Err() != nil {
		return nil, bounded.Err()
	}
	if err != nil {
		return nil, providerError(err)
	}
	if source == nil || !source.Complete || source.TVDBID != identity.TVDBID || source.SeasonNumber != season || source.Numbering != "official" || len(source.Episodes) > 64 {
		return nil, ports.ErrBadResponse
	}
	seenIDs := map[int64]bool{}
	seenNumbers := map[int]bool{}
	// Validate the full supplied snapshot before starting any per-episode lookup.
	for _, ep := range source.Episodes {
		if ep.TVDBID <= 0 || ep.SeasonNumber != season || ep.EpisodeNumber <= 0 || ep.EpisodeNumber > 10000 || seenIDs[ep.TVDBID] || seenNumbers[ep.EpisodeNumber] {
			return nil, ports.ErrBadResponse
		}
		seenIDs[ep.TVDBID], seenNumbers[ep.EpisodeNumber] = true, true
	}
	candidates := []ports.TVDBEpisodeResolution{}
	canonicalIDs := []tvdb.EpisodeIdentity{}
	for _, ep := range source.Episodes {
		resolved, err := s.resolver.ResolveTVDBEpisode(bounded, tmdbID, ep.TVDBID)
		if bounded.Err() != nil {
			return nil, bounded.Err()
		}
		if errors.Is(err, ports.ErrNotFound) {
			output.UnmatchedTVDBIDs = append(output.UnmatchedTVDBIDs, ep.TVDBID)
			continue
		}
		if err != nil {
			return nil, providerError(err)
		}
		if resolved == nil || resolved.TMDBSeriesID != tmdbID || resolved.TVDBEpisodeID != ep.TVDBID || resolved.SeasonNumber < 0 || resolved.SeasonNumber > 999 || resolved.Episode.TMDBEpisodeID <= 0 || resolved.Episode.EpisodeNumber <= 0 || resolved.Episode.EpisodeNumber > 9999 || resolved.Episode.AbsoluteNumber != nil {
			return nil, ports.ErrBadResponse
		}
		copied, err := copyEpisode(resolved.Episode)
		if err != nil {
			return nil, err
		}
		if resolved.SeasonNumber != season {
			output.UnmatchedTVDBIDs = append(output.UnmatchedTVDBIDs, ep.TVDBID)
			continue
		}
		candidates = append(candidates, ports.TVDBEpisodeResolution{TMDBSeriesID: tmdbID, TVDBEpisodeID: ep.TVDBID, SeasonNumber: season, Episode: copied})
		canonicalIDs = append(canonicalIDs, tvdb.EpisodeIdentity{ID: copied.TMDBEpisodeID, Season: season, Number: copied.EpisodeNumber})
	}
	additions, err := tvdb.SelectAdditions(season, baseIDs, canonicalIDs)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	for _, index := range additions {
		entry := candidates[index]
		copied, err := copyEpisode(entry.Episode)
		if err != nil {
			return nil, err
		}
		output.Added = append(output.Added, entry)
		output.Metadata.Episodes = append(output.Metadata.Episodes, copied)
	}
	sort.Slice(output.Metadata.Episodes, func(i, j int) bool {
		return output.Metadata.Episodes[i].EpisodeNumber < output.Metadata.Episodes[j].EpisodeNumber
	})
	return output, nil
}
func copyEpisode(value ports.TVEpisodeMetadata) (ports.TVEpisodeMetadata, error) {
	if value.AirDate != nil {
		if value.AirDate.Year() < 1 || value.AirDate.Year() > 9999 {
			return ports.TVEpisodeMetadata{}, ports.ErrBadResponse
		}
		date, err := time.Parse(time.DateOnly, value.AirDate.Format(time.DateOnly))
		if err != nil {
			return ports.TVEpisodeMetadata{}, ports.ErrBadResponse
		}
		value.AirDate = &date
	}
	if value.AbsoluteNumber != nil {
		if *value.AbsoluteNumber <= 0 {
			return ports.TVEpisodeMetadata{}, ports.ErrBadResponse
		}
		value.AbsoluteNumber = new(*value.AbsoluteNumber)
	}
	return value, nil
}
func providerError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrNotFound, ports.ErrBadResponse, ports.ErrAuthRequired, ports.ErrUnavailable, ports.ErrTimeout} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	var limited *ports.RateLimitError
	if errors.As(err, &limited) {
		return &ports.RateLimitError{RetryAfter: limited.RetryAfter}
	}
	return ports.ErrUnavailable
}
