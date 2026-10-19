package postgres

import (
	"context"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func persistCreationArtwork(ctx context.Context, q *db.Queries, mediaID uuid.UUID, p *ports.PreparedSubscriptionCreationMetadata) error {
	if p == nil || p.PosterPath == "" {
		return nil
	}
	if media.TMDBPosterURL(p.PosterPath) == "" || p.TMDBID <= 0 ||
		(p.Media.MediaType != "movie" && p.Media.MediaType != "tv") ||
		p.Media.Namespace != "tmdb."+p.Media.MediaType ||
		p.Media.ExternalID != strconv.FormatInt(p.TMDBID, 10) {
		return ports.ErrInvalidSubscriptionBindings
	}
	n, err := q.UpsertCreationMediaArtwork(ctx, db.UpsertCreationMediaArtworkParams{
		MediaID: mediaID, MediaType: p.Media.MediaType, Namespace: p.Media.Namespace,
		ExternalID: p.Media.ExternalID, PosterPath: p.PosterPath,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrSubscriptionBindingsConflict
	}
	return nil
}
