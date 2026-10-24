package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DownloadGlobalRatesOverviewStore struct{ pool *pgxpool.Pool }

var _ ports.DownloadGlobalRatesOverviewReader = (*DownloadGlobalRatesOverviewStore)(nil)

func NewDownloadGlobalRatesOverviewStore(pool *pgxpool.Pool) *DownloadGlobalRatesOverviewStore {
	return &DownloadGlobalRatesOverviewStore{pool: pool}
}

func (s *DownloadGlobalRatesOverviewStore) ReadDownloadGlobalRatesSnapshot(ctx context.Context, actor *ports.Actor) (*ports.DownloadGlobalRatesSnapshot, error) {
	if actor == nil || actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil {
		return nil, ports.ErrForbidden
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	user, err := q.GetUserByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrForbidden
	}
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	if !user.IsActive || user.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	at, err := q.LibraryOverviewSnapshotTime(ctx)
	if err != nil || !at.Valid || at.Time.IsZero() {
		return nil, ports.ErrUnavailable
	}
	rows, err := q.ListDownloadGlobalRateSources(ctx)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	related := make([]uuid.UUID, 0, len(rows)*2)
	for _, row := range rows {
		if row.LatestJobID.Valid {
			related = append(related, row.LatestJobID.Bytes)
		}
		if row.LastGoodJobID.Valid {
			related = append(related, row.LastGoodJobID.Bytes)
		}
	}
	samples, err := q.ListDownloadGlobalRateViewSamples(ctx, db.ListDownloadGlobalRateViewSamplesParams{Since: pgtype.Timestamptz{Time: at.Time.Add(-10*time.Minute - 45*time.Second), Valid: true}, RelatedJobIds: related})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.DownloadGlobalRatesSnapshot{ObservedAt: at.Time.UTC(), Sources: []ports.DownloadGlobalRateSource{}, History: []ports.DownloadGlobalRateSample{}}
	byID := make(map[uuid.UUID]*ports.DownloadGlobalRateSample, len(samples))
	for _, row := range samples {
		sample := ports.DownloadGlobalRateSample{JobID: row.JobID, ClientID: row.ClientID, ConfigStamp: row.ConfigStamp, State: row.State, FailureCode: row.FailureCode}
		if row.ObservedAt.Valid {
			v := row.ObservedAt.Time.UTC()
			sample.ObservedAt = &v
		}
		if row.FinishedAt.Valid {
			v := row.FinishedAt.Time.UTC()
			sample.FinishedAt = &v
		}
		if row.DownloadBytesPerSecond.Valid {
			v := row.DownloadBytesPerSecond.Int64
			sample.DownloadBytesPerSecond = &v
		}
		if row.UploadBytesPerSecond.Valid {
			v := row.UploadBytesPerSecond.Int64
			sample.UploadBytesPerSecond = &v
		}
		out.History = append(out.History, sample)
	}
	for i := range out.History {
		byID[out.History[i].JobID] = &out.History[i]
	}
	for _, row := range rows {
		source := ports.DownloadGlobalRateSource{ID: row.ID, Name: row.Name, ClientType: row.ClientType, ConfigStamp: row.ConfigStamp}
		if row.LatestJobID.Valid {
			source.Latest = byID[row.LatestJobID.Bytes]
			if source.Latest == nil || source.Latest.ClientID != row.ID {
				return nil, ports.ErrUnavailable
			}
		}
		if row.LastGoodJobID.Valid {
			source.LastGood = byID[row.LastGoodJobID.Bytes]
			if source.LastGood == nil || source.LastGood.ClientID != row.ID || source.LastGood.State != "succeeded" {
				return nil, ports.ErrUnavailable
			}
		}
		out.Sources = append(out.Sources, source)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
