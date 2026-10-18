package search_test

import (
	"context"
	"testing"
	"time"
)

func TestEndpointCompletionLeaseExpiresAtFinalWrite(t *testing.T) {
	f, repo, scope, queryID := endpointScopeFixture(t)
	ctx := context.Background()
	if _, e := f.pool.Exec(ctx, `CREATE FUNCTION endpoint_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.25); RETURN NEW; END $$; CREATE TRIGGER endpoint_delay BEFORE UPDATE ON search_round_endpoint FOR EACH ROW EXECUTE FUNCTION endpoint_delay()`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE job SET lease_until=clock_timestamp()+interval '150 milliseconds' WHERE id=$1`, scope.Task.Lease.ID); e != nil {
		t.Fatal(e)
	}
	before := endpointState(t, f, scope)
	if e := repo.Complete(ctx, scope, queryID); e == nil {
		t.Fatal("lease expired at final write but completed")
	}
	if endpointState(t, f, scope) != before {
		t.Fatal("expired final write committed endpoint")
	}
	if _, e := f.pool.Exec(ctx, `DROP TRIGGER endpoint_delay ON search_round_endpoint;`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE job SET lease_token=lease_token+1,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, scope.Task.Lease.ID); e != nil {
		t.Fatal(e)
	}
	scope.Task.Lease.Token++
	if e := repo.Complete(ctx, scope, queryID); e != nil {
		t.Fatal(e)
	}
}
func TestEndpointCompletionEightWayOnlyOneTerminalWrite(t *testing.T) {
	f, repo, scope, queryID := endpointScopeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 8)
	for range 8 {
		go func() { done <- repo.Complete(ctx, scope, queryID) }()
	}
	successes := 0
	for range 8 {
		if e := <-done; e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("terminal successes", successes)
	}
	before := endpointState(t, f, scope)
	if e := repo.Complete(ctx, scope, queryID); e == nil {
		t.Fatal("terminal replay accepted")
	}
	if endpointState(t, f, scope) != before {
		t.Fatal("terminal replay changed record")
	}
}
