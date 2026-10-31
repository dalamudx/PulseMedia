package transfer

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type reusePreflightBarrier struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *reusePreflightBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "-- name: GetSearchDatabaseTime :one") {
		b.once.Do(func() {
			close(b.entered)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*reusePreflightBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestLibraryReuseReceiptAppearsDuringPreflightPG(t *testing.T) {
	f, original, actor, target, file := reusePreviewFixture(t)
	command := reuseCommand(t, original, actor, target, file, "hardlink")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gate := &reusePreflightBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = gate
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	pending := &Repository{execution: NewService(pool, f.svc.idgen, f.svc.outbox, f.svc.files, transfercase.ExecutePreparedPlan)}
	release := sync.OnceFunc(func() { close(gate.release) })
	defer release()
	type result struct {
		receipt *ports.LibraryReuseReceipt
		err     error
	}
	done := make(chan result, 1)
	go func() {
		receipt, err := pending.ConfirmLibraryReuse(ctx, actor, command, "preflight-original")
		done <- result{receipt, err}
	}()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("preflight did not reach the post-receipt barrier")
	}
	winner, err := original.ConfirmLibraryReuse(ctx, actor, command, "preflight-original")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, table := range []string{"library_reuse_plan", "library_reuse_unit", "job", "audit_log", "outbox_event", "idempotency_record"} {
		counts[table] = f.count(t, table)
	}
	release()
	select {
	case got := <-done:
		if got.err != nil || got.receipt == nil || *got.receipt != *winner {
			t.Fatalf("concurrent preflight did not restore original receipt: %v", got.err)
		}
	case <-ctx.Done():
		t.Fatal("concurrent preflight did not finish")
	}
	for table, before := range counts {
		if f.count(t, table) != before {
			t.Fatalf("original receipt recovery changed %s", table)
		}
	}
}
