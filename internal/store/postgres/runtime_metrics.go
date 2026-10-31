package postgres

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RuntimeMetricsReader struct{ pool *pgxpool.Pool }

func NewRuntimeMetricsReader(pool *pgxpool.Pool) *RuntimeMetricsReader {
	return &RuntimeMetricsReader{pool: pool}
}
func (r *RuntimeMetricsReader) ReadRuntimeMetrics(ctx context.Context) (ports.RuntimeMetrics, error) {
	if r.pool == nil {
		return ports.RuntimeMetrics{}, ports.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := Queries(r.pool).ReadRuntimeQueueMetrics(ctx)
	if err != nil {
		return ports.RuntimeMetrics{}, ports.ErrUnavailable
	}
	locks, err := Queries(r.pool).ReadRuntimeLockMetrics(ctx)
	if err != nil {
		return ports.RuntimeMetrics{}, ports.ErrUnavailable
	}
	budget, err := Queries(r.pool).ReadRuntimeIndexerBudgetMetrics(ctx)
	if err != nil {
		return ports.RuntimeMetrics{}, ports.ErrUnavailable
	}
	stat := r.pool.Stat()
	snapshot := ports.RuntimeMetrics{PoolTotal: stat.TotalConns(), PoolIdle: stat.IdleConns(), PoolAcquired: stat.AcquiredConns(), PoolMax: stat.MaxConns(), AcquireCount: stat.AcquireCount(), EmptyAcquireCount: stat.EmptyAcquireCount(), CancelledAcquireCount: stat.CanceledAcquireCount(), AcquireSeconds: stat.AcquireDuration().Seconds(), EmptyAcquireSeconds: stat.EmptyAcquireWaitTime().Seconds()}
	for _, row := range rows {
		snapshot.Queues = append(snapshot.Queues, ports.QueueMetrics{Queue: row.Queue, State: row.State, Count: row.Count, ReadyAgeSeconds: row.ReadyAgeSeconds, ExpiredLeases: row.ExpiredLeases})
	}
	snapshot.LockWaiters = locks.Waiters
	snapshot.LockWaitAgeSeconds = locks.OldestWaitSeconds
	snapshot.IndexerBudget = ports.IndexerBudgetMetrics{Open: budget.Open, Interval: budget.Interval, Cooldown: budget.Cooldown, MaxDelaySeconds: budget.MaxDelaySeconds}
	return snapshot, nil
}
