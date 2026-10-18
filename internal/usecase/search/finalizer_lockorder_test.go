package search_test

import (
	"context"
	"testing"
	"time"
)

func TestFinalizerWaitingForRootDoesNotHoldEndpoints(t *testing.T) {
	f := runtimeFixture(t)
	round := f.collect(t)
	job := f.finalJob(t, round)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rootLock, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = rootLock.Exec(ctx, "SELECT id FROM library_root WHERE id=$1 FOR UPDATE", f.root); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(done)
		_, e := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(job))
		result <- e
	}()
	defer func() {
		_ = rootLock.Rollback(context.Background())
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("finalizer did not join")
		}
	}()
	waiting := false
	for !waiting {
		if e = f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%library_root%' AND pid<>pg_backend_pid())").Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if !waiting {
			select {
			case <-done:
				t.Fatal("finalizer exited before root wait")
			case <-ctx.Done():
				t.Fatal("root wait not observed")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	probe, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = probe.Exec(ctx, "SELECT id FROM indexer WHERE id=$1 FOR UPDATE NOWAIT", f.idx); e != nil {
		_ = probe.Rollback(context.Background())
		t.Fatal("root waiter held indexer: lock order inversion", e)
	}
	if _, e = probe.Exec(ctx, "SELECT id FROM download_client WHERE id=$1 FOR NO KEY UPDATE NOWAIT", f.downloader); e != nil {
		_ = probe.Rollback(context.Background())
		t.Fatal("root waiter held client share lock", e)
	}
	if e = probe.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if e = rootLock.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e != nil {
		t.Fatal("finalizer failed after lock released", e)
	}
}
