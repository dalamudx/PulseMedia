package postgres

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type HistoryRetentionStore struct{ pool *pgxpool.Pool }
type HistoryPruneStats struct{ Observations, Jobs, Candidates int64 }

func NewHistoryRetentionStore(pool *pgxpool.Pool) *HistoryRetentionStore {
	return &HistoryRetentionStore{pool}
}

func (s *HistoryRetentionStore) PruneHistory(ctx context.Context) (out HistoryPruneStats, err error) {
	defer func() {
		if err != nil {
			out = HistoryPruneStats{}
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	if s == nil || s.pool == nil {
		return out, ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err = q.SetHistoryRetentionBudget(ctx); err != nil {
		return out, err
	}
	ids, err := q.PruneGlobalRateObservations(ctx)
	if err != nil {
		return out, err
	}
	out.Observations = int64(len(ids))
	if len(ids) > 0 {
		if out.Jobs, err = q.PruneGlobalRateJobs(ctx, ids); err != nil {
			return out, err
		}
		if out.Jobs != out.Observations {
			return out, errors.New("observation cleanup graph mismatch")
		}
	}
	if out.Candidates, err = q.PruneUnreferencedReleaseCandidates(ctx); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}
