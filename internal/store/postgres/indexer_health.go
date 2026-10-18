package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IndexerHealthStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewIndexerHealthStore(pool *pgxpool.Pool, ids ports.IDGen) *IndexerHealthStore {
	return &IndexerHealthStore{pool, ids}
}

var _ ports.IndexerHealthRepository = (*IndexerHealthStore)(nil)

func (s *IndexerHealthStore) ListHealth(ctx context.Context) ([]ports.IndexerHealthInfo, error) {
	rows, e := db.New(s.pool).ListIndexers(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := []ports.IndexerHealthInfo{}
	for _, r := range rows {
		item := ports.IndexerHealthInfo{ID: r.ID, Name: r.Name, URL: r.Url, Health: r.Health, Reason: r.HealthReason, ConfigVersion: r.ConfigVersion, CreatedAt: r.CreatedAt.Time, NextCheckAt: r.HealthNextCheckAt.Time, Failures: r.HealthFailures}
		if r.HealthCheckedAt.Valid {
			value := r.HealthCheckedAt.Time
			item.CheckedAt = &value
		}
		if r.HealthObservedAt.Valid {
			value := r.HealthObservedAt.Time
			item.ObservedAt = &value
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *IndexerHealthStore) QueueHealth(ctx context.Context, limit int32) (int, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	rows, e := q.ListDueHealthIndexers(ctx, limit)
	if e != nil {
		return 0, e
	}
	n := 0
	for _, row := range rows {
		count, e := q.QueueIndexerHealth(ctx, db.QueueIndexerHealthParams{ID: s.ids.NewUUID(), ID_2: row.ID})
		if e != nil {
			return 0, e
		}
		n += int(count)
		if e = q.AdvanceIndexerHealthSchedule(ctx, row.ID); e != nil {
			return 0, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return 0, e
	}
	return n, nil
}
func (s *IndexerHealthStore) authorize(ctx context.Context, q *db.Queries, j ports.IndexerHealthJob) error {
	row, e := q.GetAuthorizedSearchJob(ctx, db.GetAuthorizedSearchJobParams{ID: j.ID, Kind: "indexer_health", LeaseOwner: pgtype.Text{String: j.Owner, Valid: true}, LeaseToken: j.Token})
	if e != nil {
		return ports.ErrUnavailable
	}
	var payload ports.IndexerHealthJob
	if json.Unmarshal(row.Payload, &payload) != nil || payload.IndexerID != j.IndexerID || payload.ConfigVersion != j.ConfigVersion {
		return ports.ErrBadResponse
	}
	return nil
}
func (s *IndexerHealthStore) CheckHealth(ctx context.Context, j ports.IndexerHealthJob) (bool, error) {
	q := db.New(s.pool)
	if e := s.authorize(ctx, q, j); e != nil {
		return false, e
	}
	row, e := q.GetIndexerByID(ctx, j.IndexerID)
	if e != nil {
		return false, e
	}
	return row.ConfigVersion != j.ConfigVersion, nil
}
func (s *IndexerHealthStore) observation(ctx context.Context, q *db.Queries, id uuid.UUID, version int64, query uuid.UUID) error {
	r, e := q.GetQueryExecution(ctx, query)
	if e != nil {
		return e
	}
	if r.IndexerID != id || r.ConfigVersion != version || (r.State != "succeeded" && r.State != "failed") {
		return ports.ErrBadResponse
	}
	reason := "unavailable"
	if r.State == "succeeded" {
		reason = "ok"
	} else if r.Error.String == "indexer rate limited" {
		reason = "rate_limited"
	}
	row, e := q.RecordIndexerHealth(ctx, db.RecordIndexerHealthParams{ID: id, ConfigVersion: version, Reason: reason, ObservedAt: r.FinishedAt, QueryID: pgtype.UUID{Bytes: r.ID, Valid: true}})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	safe, _ := json.Marshal(map[string]any{"indexer_id": id, "config_version": version, "query_id": r.ID, "observed_at": r.FinishedAt.Time, "health": row.Health, "reason": reason})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: "indexer-health", Action: "indexer.health_observed", TargetType: "indexer", TargetID: id.String(), Payload: safe}); e != nil {
		return e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "indexer", AggregateID: id.String()}); e != nil {
		return e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "indexer", AggregateID: id.String()})
	if e != nil {
		return e
	}
	_, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "indexer", AggregateID: id.String(), Seq: int64(seq), EventType: "indexer.health_observed", Payload: safe})
	return e
}
func (s *IndexerHealthStore) CompleteHealth(ctx context.Context, j ports.IndexerHealthJob, query *uuid.UUID) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	row, e := q.LockRSSIndexer(ctx, j.IndexerID)
	if e != nil {
		return e
	}
	if _, e = q.LockJobForWrite(ctx, j.ID); e != nil {
		return e
	}
	if e = s.authorize(ctx, q, j); e != nil {
		return e
	}
	if query != nil && row.ConfigVersion == j.ConfigVersion {
		if e = s.observation(ctx, q, j.IndexerID, j.ConfigVersion, *query); e != nil {
			return e
		}
	}
	if _, e = q.FinishJob(ctx, db.FinishJobParams{Column1: j.ID, Column2: j.Owner, Column3: j.Token, Column4: []byte(`{}`)}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *IndexerHealthStore) ReportManualHealth(ctx context.Context, id uuid.UUID, version int64, query uuid.UUID) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	row, e := q.LockRSSIndexer(ctx, id)
	if e != nil {
		return e
	}
	if row.ConfigVersion != version {
		return ports.ErrUnavailable
	}
	if e = s.observation(ctx, q, id, version, query); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
