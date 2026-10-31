package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTVEpisodeDiscoveryReadConcurrentAccountUpdatePG(t *testing.T) {
	f, store, key := tvEpisodeDiscoveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if scheduled, err := store.ScheduleTVEpisodeDiscovery(ctx, key); err != nil || !scheduled {
		t.Fatalf("schedule: %t %v", scheduled, err)
	}
	original, err := store.ReadTVEpisodeDiscovery(ctx, f.actor, key)
	if err != nil || original.SearchID == nil {
		t.Fatal("original status", err)
	}
	before := tvEpisodeDiscoveryState(t, f)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `UPDATE app_user SET role=role WHERE id=$1`, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	blocker := tx.Conn().PgConn().PID()
	type result struct {
		status *ports.TVEpisodeDiscoveryStatus
		err    error
	}
	returned, done := make(chan result, 1), make(chan struct{})
	go func() {
		defer close(done)
		status, e := store.ReadTVEpisodeDiscovery(ctx, f.actor, key)
		returned <- result{status, e}
	}()
	defer func() { cancel(); _ = tx.Rollback(context.Background()); <-done }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if e := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case r := <-returned:
			t.Fatalf("read returned before the blocking account lock: %v", r.err)
		case <-ctx.Done():
			t.Fatal("reader did not reach real account lock", ctx.Err())
		case <-tick.C:
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-returned:
		if r.err != nil || r.status == nil || r.status.SearchID == nil || *r.status.SearchID != *original.SearchID || r.status.CurrentFileID == nil || *r.status.CurrentFileID != f.request.CurrentFileID || r.status.ExecutionAllowed {
			t.Fatalf("concurrent read lost original observation: status=%+v err=%v", r.status, r.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if tvEpisodeDiscoveryState(t, f) != before {
		t.Fatal("read retry changed business state")
	}
}
