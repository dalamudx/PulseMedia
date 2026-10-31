package postgres

import (
	"context"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The two-integer lock namespace is separate from existing hashed resource gates.
const physicalConnectionNamespace int32 = 0x504d5048

type physicalPoolConnection = pgxpool.Conn

// PhysicalConnection keeps a bounded database session until physical I/O returns.
// Cancellation of the caller never releases a returned connection.
type PhysicalConnection struct {
	*physicalPoolConnection
	slot int32
	once sync.Once
}

// AcquirePhysicalConnection reserves at least half of the pool for short UoWs,
// authentication and heartbeats. Database locks share admission across stores
// without a process-global registry. Waiting never retains a pool connection.
func AcquirePhysicalConnection(ctx context.Context, pool *pgxpool.Pool) (*PhysicalConnection, error) {
	if pool == nil || pool.Config().MaxConns < 2 {
		return nil, ports.ErrUnavailable
	}
	limit := min(pool.Config().MaxConns/2, 4)
	for {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		for slot := int32(0); slot < limit; slot++ {
			var held bool
			err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1::integer,$2::integer)", physicalConnectionNamespace, slot).Scan(&held)
			if err != nil {
				clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = conn.Hijack().Close(clean)
				cancel()
				return nil, err
			}
			if held {
				return &PhysicalConnection{physicalPoolConnection: conn, slot: slot}, nil
			}
		}
		conn.Release()
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *PhysicalConnection) Release() {
	c.once.Do(func() {
		if c.Conn().IsClosed() {
			c.physicalPoolConnection.Release()
			return
		}
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		err := c.QueryRow(clean, "SELECT pg_advisory_unlock($1::integer,$2::integer)", physicalConnectionNamespace, c.slot).Scan(&released)
		if err != nil || !released {
			_ = c.physicalPoolConnection.Hijack().Close(clean)
			return
		}
		c.physicalPoolConnection.Release()
	})
}

// Hijack transfers all gate ownership to the raw connection; closing it releases
// both admission and resource locks, including an uncertain unlock result.
func (c *PhysicalConnection) Hijack() *pgx.Conn {
	var raw *pgx.Conn
	c.once.Do(func() { raw = c.physicalPoolConnection.Hijack() })
	return raw
}
