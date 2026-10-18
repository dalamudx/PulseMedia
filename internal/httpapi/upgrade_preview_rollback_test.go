package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rollbackBlackholeConn leaves a healthy PostgreSQL transaction open while the
// client waits for a ROLLBACK response which will never arrive.
type rollbackBlackholeConn struct {
	net.Conn
	seen chan struct{}
	once sync.Once
}

func (c *rollbackBlackholeConn) Write(p []byte) (int, error) {
	if len(p) > 5 && p[0] == 'Q' && bytes.Equal(p[5:], []byte("rollback\x00")) {
		c.once.Do(func() { close(c.seen) })
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func TestUpgradePreviewPGRollbackHasIndependentDeadline(t *testing.T) {
	f := newUpgradeFixture(t)
	cfg := f.a.DBPool().Config().Copy()
	cfg.MinConns, cfg.MaxConns = 0, 1
	dial := cfg.ConnConfig.DialFunc
	seen := make(chan struct{})
	var mu sync.Mutex
	var connections []net.Conn
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		connections = append(connections, conn)
		mu.Unlock()
		return &rollbackBlackholeConn{Conn: conn, seen: seen}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := f.request
	req.SubscriptionID = uuid.New()
	done := make(chan error, 1)
	go func() {
		_, readErr := postgres.NewUpgradePreviewStore(pool).ReadUpgradePreview(ctx, f.actor, req)
		done <- readErr
	}()
	select {
	case <-seen:
	case err = <-done:
		t.Fatalf("healthy early return did not attempt rollback: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("did not reach rollback on a healthy connection")
	}
	cancelledAt := time.Now()
	cancel()
	select {
	case err = <-done:
		if elapsed := time.Since(cancelledAt); elapsed < 4*time.Second {
			t.Fatalf("rollback reused parent cancellation or shortened its independent five-second budget: %s", elapsed)
		}
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("lost the original missing-subscription result: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("unresponsive rollback ignored cancellation without an independent cleanup deadline")
	}
}
