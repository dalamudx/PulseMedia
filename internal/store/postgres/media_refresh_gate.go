package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"time"
)

// acquireMediaServerPermit retains physical media server exclusion until Release,
// regardless of the caller's logical lease/context. No network I/O is in a tx.
func acquireMediaServerPermit(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var held bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended('mediaserver.refresh:' || $1::text,0))", id.String()).Scan(&held)
	if err != nil || !held {
		conn.Release()
		if err != nil {
			return nil, err
		}
		return nil, ports.ErrUnavailable
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			err := conn.QueryRow(cleanup, "SELECT pg_advisory_unlock(hashtextextended('mediaserver.refresh:' || $1::text,0))", id.String()).Scan(&unlocked)
			if err != nil || !unlocked {
				raw := conn.Hijack()
				_ = raw.Close(cleanup)
				return
			}
			conn.Release()
		})
	}, nil
}
