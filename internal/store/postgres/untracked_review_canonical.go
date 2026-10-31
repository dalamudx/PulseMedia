package postgres

import (
	"context"
	"errors"
	"sort"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Current validation is deliberately separate from immutable receipt verification.
func reviewCanonicalSelection(ctx context.Context, tx pgx.Tx, in ports.UntrackedReviewInput, lock bool) error {
	if !in.CanonicalSelectionValid() {
		return ports.ErrRejected
	}
	if in.CanonicalUnitID == "" {
		return nil
	}
	q := db.New(tx)
	work, err := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.tv", ExternalID: strconv.FormatInt(in.TMDBID, 10)})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrUntrackedReviewStale
	}
	if err != nil {
		return err
	}
	if work.MediaType != "tv" || work.MetadataVersion != in.MediaMetadataVersion {
		return ports.ErrUntrackedReviewStale
	}
	if lock {
		m, e := q.LockMappingMedia(ctx, work.ID)
		if e != nil {
			return e
		}
		if m.MetadataVersion != in.MediaMetadataVersion || m.MediaType != "tv" {
			return ports.ErrUntrackedReviewStale
		}
	}
	u, err := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: work.ID, UnitKey: media.FormatEpisodeUnitKey(in.Season, in.Episode)})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrUntrackedReviewStale
	}
	if err != nil {
		return err
	}
	if lock {
		u, err = q.LockTransferUnit(ctx, u.ID)
		if err != nil {
			return err
		}
	}
	if u.ID.String() != in.CanonicalUnitID || u.MediaID != work.ID || u.MetadataVersion != in.UnitMetadataVersion || u.Retired || !u.SeasonNumber.Valid || int(u.SeasonNumber.Int32) != in.Season || !u.EpisodeNumber.Valid || int(u.EpisodeNumber.Int32) != in.Episode {
		return ports.ErrUntrackedReviewStale
	}
	identity, err := q.GetTMDBEpisodeIdentityForUnit(ctx, u.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrUntrackedReviewStale
	}
	if err != nil {
		return err
	}
	if identity.ExternalID != in.TMDBEpisodeID || identity.TmdbSeriesID != in.TMDBID {
		return ports.ErrUntrackedReviewStale
	}
	return nil
}

func (s *UntrackedReviewStore) ReadUntrackedReviewEpisodeOptions(ctx context.Context, actor *ports.Actor, scope, file uuid.UUID, tmdbID int64, season int) (out []ports.UntrackedReviewEpisodeOption, err error) {
	defer func() { err = reviewError(ctx, err) }()
	a, err := reviewActor(actor)
	if err != nil {
		return nil, err
	}
	if scope == uuid.Nil || file == uuid.Nil || tmdbID < 1 || season < 1 || season > 99 {
		return nil, ports.ErrRejected
	}
	tx, err := s.begin(ctx, a, true)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	c, f, err := reviewOwned(ctx, tx, uuid.MustParse(a.ID), scope, file, false)
	if err != nil {
		return nil, err
	}
	if !c.Enabled || c.TargetMediaType != "tv" || f.ScopeVersion != c.Version {
		return nil, ports.ErrUntrackedReviewStale
	}
	q := db.New(tx)
	if _, err = discoveryMaterial(ctx, q, *c); err != nil {
		return nil, err
	}
	work, err := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.tv", ExternalID: strconv.FormatInt(tmdbID, 10)})
	if errors.Is(err, pgx.ErrNoRows) {
		return []ports.UntrackedReviewEpisodeOption{}, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if work.MediaType != "tv" || work.MetadataVersion < 1 {
		return nil, ports.ErrUntrackedReviewStale
	}
	units, err := q.ListTMDBEpisodeUnits(ctx, work.ID)
	if err != nil {
		return nil, err
	}
	out = []ports.UntrackedReviewEpisodeOption{}
	for _, u := range units {
		if u.Retired || u.MetadataVersion < 1 || !u.SeasonNumber.Valid || int(u.SeasonNumber.Int32) != season || !u.EpisodeNumber.Valid || u.EpisodeNumber.Int32 < 1 || u.EpisodeNumber.Int32 > 9999 || !u.TmdbEpisodeID.Valid || u.TmdbEpisodeID.Int64 < 1 || !u.TmdbSeriesID.Valid || u.TmdbSeriesID.Int64 != tmdbID || u.UnitKey != media.FormatEpisodeUnitKey(season, int(u.EpisodeNumber.Int32)) {
			continue
		}
		out = append(out, ports.UntrackedReviewEpisodeOption{CanonicalUnitID: u.ID.String(), MediaMetadataVersion: work.MetadataVersion, UnitMetadataVersion: u.MetadataVersion, Season: season, Episode: int(u.EpisodeNumber.Int32), TMDBEpisodeID: u.TmdbEpisodeID.Int64, UnitKey: u.UnitKey, Title: work.Title})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Episode < out[j].Episode })
	if err = reviewReadActor(ctx, tx, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
