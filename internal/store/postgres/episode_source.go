package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type EpisodeSourceStore struct{ refresh *TVRefreshStore }

func NewEpisodeSourceStore(refresh *TVRefreshStore) *EpisodeSourceStore {
	return &EpisodeSourceStore{refresh}
}
func readEpisodeSourceContext(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID) (*ports.EpisodeSourceContext, error) {
	hint, e := readTVRefresh(ctx, q, a, id)
	if e != nil {
		return nil, e
	}
	sub, e := q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	units, e := q.ListTMDBEpisodeUnits(ctx, hint.MediaID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.EpisodeSourceContext{SubscriptionID: id, MediaID: hint.MediaID, TMDBID: hint.TMDBID, MediaVersion: hint.MediaVersion, IntentVersion: hint.IntentVersion, MappingRevision: sub.EpisodeMappingRevision.Int64, Units: []ports.EpisodeSourceUnit{}}
	for _, u := range units {
		source := int64(0)
		if u.TmdbEpisodeID.Valid && u.TmdbSeriesID.Valid && u.TmdbSeriesID.Int64 == hint.TMDBID {
			source = u.TmdbEpisodeID.Int64
		}
		season, number := -1, -1
		if u.SeasonNumber.Valid {
			season = int(u.SeasonNumber.Int32)
		}
		if u.EpisodeNumber.Valid {
			number = int(u.EpisodeNumber.Int32)
		}
		out.Units = append(out.Units, ports.EpisodeSourceUnit{ID: u.ID, EpisodeID: source, Version: u.MetadataVersion, Key: u.UnitKey, Season: season, Episode: number, Retired: u.Retired})
	}
	if e = readCompleteSeasonMetadata(ctx, q, out); e != nil {
		return nil, ports.ErrUnavailable
	}
	raw, _ := json.Marshal(out)
	sum := sha256.Sum256(raw)
	out.InputStamp = hex.EncodeToString(sum[:])
	return out, nil
}
func (s *EpisodeSourceStore) ReadEpisodeSourceContext(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.EpisodeSourceContext, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.refresh.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	out, e := readEpisodeSourceContext(ctx, q, a, id)
	if e != nil {
		return nil, e
	}
	if tx.Commit(ctx) != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
