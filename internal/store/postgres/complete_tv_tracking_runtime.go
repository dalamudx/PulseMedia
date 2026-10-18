package postgres

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CompleteTVTrackingRuntimeStore only schedules jobs. It never changes task facts,
// acquires/releases physical permits, or performs network/file I/O.
type CompleteTVTrackingRuntimeStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewCompleteTVTrackingRuntimeStore(pool *pgxpool.Pool, ids ports.IDGen) *CompleteTVTrackingRuntimeStore {
	return &CompleteTVTrackingRuntimeStore{pool: pool, ids: ids}
}

var _ ports.CompleteTVTrackingScheduler = (*CompleteTVTrackingRuntimeStore)(nil)

// Successful partial observations get a fresh job no sooner than 30 seconds
// after commit. Errors use the queue's three attempts; expired jobs get at most
// three generations since the last successful partial observation. Exhaustion
// requires review; a retained physical permit is never reclaimed or bypassed.
func (s *CompleteTVTrackingRuntimeStore) ScheduleCompleteTVTracking(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	held, err := q.LockCompleteTVTrackingScheduler(ctx)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	if err = q.ExpireCompleteTVTrackingJobs(ctx); err != nil {
		return err
	}
	work, err := q.ListDueCompleteTVTracking(ctx)
	if err != nil {
		return err
	}
	for _, w := range work {
		err = q.EnqueueCompleteTVTracking(ctx, db.EnqueueCompleteTVTrackingParams{ID: s.ids.NewUUID(), DedupeKey: "complete-tv-track:" + w.TaskID.String(), TaskID: w.TaskID.String(), OperationID: w.OperationID.String()})
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
