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

type EvaluationScheduleStore struct {
	queries *db.Queries
	pool    *pgxpool.Pool
}

var _ ports.EvaluationScheduleRepository = (*EvaluationScheduleStore)(nil)

func NewEvaluationScheduleStore(pool *pgxpool.Pool) *EvaluationScheduleStore {
	return &EvaluationScheduleStore{queries: Queries(pool), pool: pool}
}

func (s *EvaluationScheduleStore) ListPendingEvaluations(ctx context.Context, limit int32) ([]ports.EvaluationScheduleWanted, error) {
	rows, err := s.queries.ListPendingEvaluationWantedItems(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ports.EvaluationScheduleWanted, 0, len(rows))
	for _, row := range rows {
		item := ports.EvaluationScheduleWanted{ID: row.ID, SubscriptionID: row.SubscriptionID}
		if row.NextEvaluateAt.Valid {
			at := row.NextEvaluateAt.Time
			item.NextEvaluateAt = &at
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *EvaluationScheduleStore) LoadEvaluationSubscription(ctx context.Context, id uuid.UUID) (ports.EvaluationScheduleSubscription, error) {
	row, err := s.queries.GetSubscriptionByID(ctx, id)
	if err != nil {
		return ports.EvaluationScheduleSubscription{}, err
	}
	return ports.EvaluationScheduleSubscription{UserID: row.UserID, QualityPolicyID: row.QualityPolicyID, QualityPolicyRevision: row.QualityPolicyRevision}, nil
}

func (s *EvaluationScheduleStore) EvaluationDatabaseTime(ctx context.Context) (time.Time, error) {
	now, err := s.queries.GetSearchDatabaseTime(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if !now.Valid {
		return time.Time{}, ports.ErrUnavailable
	}
	return now.Time, nil
}

func (s *EvaluationScheduleStore) HasFreshEvaluationRSS(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.queries.HasFreshWantedRSSObservation(ctx, id)
}

func (s *EvaluationScheduleStore) ConsumeEvaluationRSS(ctx context.Context, id uuid.UUID, observed time.Time) error {
	return s.queries.ConsumeWantedRSSObservation(ctx, db.ConsumeWantedRSSObservationParams{ID: id, Column2: pgtype.Timestamptz{Time: observed, Valid: true}})
}

func (s *EvaluationScheduleStore) HasActiveEvaluationRound(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := s.queries.GetActiveSearchRoundForWanted(ctx, id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return err == nil, err
	}
	// Historical repository fixtures retain their original schema. Production
	// requires the current schema, while every actual read error remains fatal.
	var available bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('automatic_tv_round') IS NOT NULL`).Scan(&available); err != nil {
		return false, err
	}
	if !available {
		return false, nil
	}
	var active bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM automatic_tv_round WHERE wanted_id=$1 AND state='running')`, id).Scan(&active)
	return active, err
}

func (s *EvaluationScheduleStore) RescheduleEvaluation(ctx context.Context, id uuid.UUID, delay time.Duration) error {
	return s.queries.RescheduleWantedEvaluation(ctx, db.RescheduleWantedEvaluationParams{ID: id, DelayMs: delay.Milliseconds()})
}
