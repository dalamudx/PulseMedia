package search_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"sync/atomic"
	"testing"
	"time"
)

func endpointTask(t *testing.T, job *db.Job) ports.SearchEndpointTask {
	t.Helper()
	var task ports.SearchEndpointTask
	if err := json.Unmarshal(job.Payload, &task); err != nil {
		t.Fatal(err)
	}
	task.Lease = ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}
	return task
}

type endpointScopeClient struct{ calls atomic.Int32 }

func (c *endpointScopeClient) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	c.calls.Add(1)
	return nil, nil
}
func (*endpointScopeClient) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrUnavailable
}
func TestEndpointTaskCannotBorrowAnotherEndpointScope(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	other, e := f.svc.CreateIndexer(ctx, "other scoped endpoint", "https://scope.invalid", "synthetic", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE subscription SET indexer_ids=jsonb_build_array($2::text,$3::text) WHERE id=$1", f.sub, f.idx.String(), other.ID.String()); e != nil {
		t.Fatal(e)
	}
	round, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	job, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "scope-owner", time.Minute)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	var body struct {
		RoundID   uuid.UUID `json:"round_id"`
		IndexerID uuid.UUID `json:"indexer_id"`
	}
	if e = json.Unmarshal(job.Payload, &body); e != nil {
		t.Fatal(e)
	}
	target := f.idx
	if body.IndexerID == target {
		target = other.ID
	}
	c := &endpointScopeClient{}
	f.svc.RegisterClient(target, c)
	forged := *job
	forged.Payload, _ = json.Marshal(map[string]uuid.UUID{"round_id": round.ID, "indexer_id": target})
	e = f.svc.ExecuteEndpointJob(ctx, endpointTask(t, &forged))
	var state string
	var queries, pending int
	if x := f.pool.QueryRow(ctx, `SELECT (SELECT state FROM job WHERE id=$1),(SELECT count(*) FROM indexer_query),(SELECT count(*) FROM search_round_endpoint WHERE round_id=$2 AND state='pending')`, job.ID, round.ID).Scan(&state, &queries, &pending); x != nil {
		t.Fatal(x)
	}
	if e == nil || c.calls.Load() != 0 || queries != 0 || state != "running" || pending != 2 {
		t.Fatalf("borrowed task scope: error=%v calls=%d queries=%d state=%s pending=%d", e, c.calls.Load(), queries, state, pending)
	}
}
