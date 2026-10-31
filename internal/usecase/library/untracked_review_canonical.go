package library

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func (s *UntrackedReviewService) EpisodeOptions(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, tmdbID int64, season int) ([]ports.UntrackedReviewEpisodeOption, error) {
	a, err := untrackedReviewActor(ctx, actor, scope, file, "episode-options")
	if err != nil {
		return nil, err
	}
	if tmdbID < 1 || season < 1 || season > 99 {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	repo, ok := s.repo.(ports.UntrackedReviewEpisodeRepository)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	options, err := repo.ReadUntrackedReviewEpisodeOptions(ctx, &a, scope, file, tmdbID, season)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedReviewError(ctx, err)
	}
	if options == nil || len(options) > 5000 {
		return nil, ports.ErrUnavailable
	}
	seen := map[string]bool{}
	previous := 0
	for _, o := range options {
		in := ports.UntrackedReviewInput{MediaType: "tv", TMDBID: tmdbID, CanonicalUnitID: o.CanonicalUnitID, MediaMetadataVersion: o.MediaMetadataVersion, UnitMetadataVersion: o.UnitMetadataVersion, Season: o.Season, Episode: o.Episode, TMDBEpisodeID: o.TMDBEpisodeID}
		if !in.CanonicalSelectionValid() || o.Season != season || o.Episode <= previous || seen[o.CanonicalUnitID] {
			return nil, ports.ErrUnavailable
		}
		seen[o.CanonicalUnitID], previous = true, o.Episode
	}
	return append([]ports.UntrackedReviewEpisodeOption{}, options...), nil
}
