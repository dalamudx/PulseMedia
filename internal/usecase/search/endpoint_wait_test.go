package search_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
	"time"
)

func TestEndpointCompletionRechecksAuthorityAfterRoundWait(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"owner", `UPDATE job SET lease_owner='replacement' WHERE id=$1`},
		{"token", `UPDATE job SET lease_token=lease_token+1 WHERE id=$1`},
		{"state", `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`},
		{"expiry", `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`},
		{"scope", `UPDATE job SET payload=jsonb_set(payload,'{round_id}',to_jsonb(gen_random_uuid()::text)) WHERE id=$1`},
		{"dedupe", `UPDATE job SET dedupe_key='wrong persisted endpoint' WHERE id=$1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, repo, scope, queryID := endpointScopeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			blocker, e := f.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = blocker.Rollback(ctx) }()
			var pid int
			if e = blocker.QueryRow(ctx, "SELECT pg_backend_pid() FROM search_round WHERE id=$1 FOR UPDATE", scope.Task.RoundID).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- repo.Complete(ctx, scope, queryID) }()
			defer func() {
				cancel()
				_ = blocker.Rollback(context.Background())
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("endpoint completion did not drain after cancellation")
				}
			}()
			waitCtx, waitCancel := context.WithTimeout(ctx, time.Second)
			defer waitCancel()
			for {
				var waiting bool
				if e = f.pool.QueryRow(waitCtx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting); e != nil {
					t.Fatal(e)
				}
				if waiting {
					break
				}
				select {
				case <-waitCtx.Done():
					t.Fatal("completion did not wait on round")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if _, e = blocker.Exec(ctx, tc.sql, scope.Task.Lease.ID); e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			before := endpointState(t, f, scope)
			select {
			case e = <-done:
				if e == nil {
					t.Fatal("expired or replaced authority committed")
				}
				done <- e
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if endpointState(t, f, scope) != before {
				t.Fatal("rejected writer changed persisted task/endpoint")
			}
		})
	}
}
func TestEndpointCompletionRejectsFrozenSnapshotChange(t *testing.T) {
	f, repo, scope, queryID := endpointScopeFixture(t)
	ctx := context.Background()
	if _, e := f.pool.Exec(ctx, `UPDATE search_round SET decision_snapshot=jsonb_set(decision_snapshot,'{title}','"Other title"') WHERE id=$1`, scope.Task.RoundID); e != nil {
		t.Fatal(e)
	}
	before := endpointState(t, f, scope)
	if e := repo.Complete(ctx, scope, queryID); e == nil {
		t.Fatal("changed snapshot accepted")
	}
	if endpointState(t, f, scope) != before {
		t.Fatal("changed snapshot altered task")
	}
	if _, e := repo.Load(ctx, ports.SearchEndpointTask{Lease: scope.Task.Lease, RoundID: scope.Task.RoundID}); e == nil {
		t.Fatal("missing indexer accepted")
	}
}
