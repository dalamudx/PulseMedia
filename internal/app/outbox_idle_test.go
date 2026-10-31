package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type idleOutboxTrace struct{ claims atomic.Int64 }

func (t *idleOutboxTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "-- name: ClaimOutboxDelivery") {
		t.claims.Add(1)
	}
	return ctx
}
func (*idleOutboxTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestOutboxIdleExitAndDueRestartPG(t *testing.T) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); w.WriteHeader(200) }))
	defer server.Close()
	channel := createOutboxChannel(t, f, n, server.URL)
	consumer := "notify:" + channel.String()
	a := &App{dbPool: pool, idgen: f.gen, logger: logging.NewLogger(io.Discard, "error")}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// An empty direct worker must return without a cancellation signal.
	empty := make(chan struct{})
	go func() {
		defer close(empty)
		a.runOutboxConsumer(ctx, ob, ms, n, make(chan struct{}, 4), consumer, "notify", channel)
	}()
	select {
	case <-empty:
	case <-ctx.Done():
		t.Fatal("empty consumer retained")
	}
	publish := func(delay bool) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		event, err := ob.Publish(ctx, db.New(tx), "library", uuid.NewString(), "media.imported", map[string]string{"title": "fixture"}, []string{consumer})
		if err != nil {
			t.Fatal(err)
		}
		if delay {
			if _, err = tx.Exec(ctx, "UPDATE outbox_delivery SET not_before=clock_timestamp()+interval '3 seconds' WHERE event_id=$1", event.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); a.runOutboxWorkers(ctx, ob, ms, n) }()
	defer func() { cancel(); <-done }()
	delivered := func(want int) bool {
		var count int
		err := pool.QueryRow(ctx, "SELECT count(*) FROM outbox_delivery WHERE state='delivered'").Scan(&count)
		return err == nil && count == want
	}
	publish(false)
	waitOutboxCondition(t, ctx, func() bool { return delivered(1) })
	wait := func(d time.Duration) {
		t.Helper()
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal("worker deadline")
		}
	}
	wait(1500 * time.Millisecond)
	claims := trace.claims.Load()
	wait(2200 * time.Millisecond)
	if trace.claims.Load() != claims {
		t.Fatal("completed consumer still polls", claims, trace.claims.Load())
	}
	publish(true)
	wait(time.Second)
	if trace.claims.Load() != claims {
		t.Fatal("future delivery started consumer early")
	}
	waitOutboxCondition(t, ctx, func() bool { return delivered(2) })
	if sends.Load() != 2 {
		t.Fatal("restart duplicated or lost sends", sends.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not join")
	}
}
