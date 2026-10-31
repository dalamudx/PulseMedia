package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOutboxDisabledChannelDoesNotChurnAndResumesPG(t *testing.T) {
	f := organizationSetup(t)
	trace := &idleOutboxTrace{}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f.pool = pool
	f.q = db.New(pool)
	ob, ms, n := testOutboxServices(f)
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { sends.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	channel := createOutboxChannel(t, f, n, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err = pool.Exec(ctx, "UPDATE notification_channel SET is_enabled=false WHERE id=$1", channel); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	event, err := ob.Publish(ctx, db.New(tx), "library", uuid.NewString(), "media.imported", nil, []string{"notify:" + channel.String()})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{dbPool: pool, idgen: f.gen, logger: logging.NewLogger(io.Discard, "error")}
	done := make(chan struct{})
	go func() { defer close(done); a.runOutboxWorkers(ctx, ob, ms, n) }()
	defer func() { cancel(); <-done }()
	timer := time.NewTimer(2300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if trace.claims.Load() != 0 || sends.Load() != 0 {
		t.Fatalf("disabled consumer churned: claims=%d sends=%d", trace.claims.Load(), sends.Load())
	}
	var attempts int
	if err = pool.QueryRow(ctx, "SELECT attempts FROM outbox_delivery WHERE event_id=$1", event.ID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("disabled attempt changed: %d %v", attempts, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE notification_channel SET is_enabled=true WHERE id=$1", channel); err != nil {
		t.Fatal(err)
	}
	waitOutboxCondition(t, ctx, func() bool {
		var state string
		err := pool.QueryRow(ctx, "SELECT state FROM outbox_delivery WHERE event_id=$1", event.ID).Scan(&state)
		return err == nil && state == "delivered"
	})
	if sends.Load() != 1 {
		t.Fatalf("re-enable sends=%d", sends.Load())
	}
}
