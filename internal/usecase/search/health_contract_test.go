package search_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentHealthProbeSafety(t *testing.T) {
	f := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids := idgen.New()
	repo := postgres.NewIndexerHealthStore(f.pool, ids)
	health := search.NewHealthService(repo, f.svc)
	queue := jobs.NewQueue(f.pool, ids, clock.New())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := f.pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("UPDATE indexer SET health='offline',health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", f.idx)
	var total atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); n, e := health.QueueDue(ctx, 20); total.Add(int64(n)); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if total.Load() != 1 {
		t.Fatal("parallel health reconstruction duplicated jobs", total.Load())
	}
	claim := func() ports.IndexerHealthJob {
		t.Helper()
		j, e := queue.Claim(ctx, []string{"indexer_health"}, "health-owner", 45*time.Second)
		if e != nil || j == nil {
			t.Fatal(j, e)
		}
		var p ports.IndexerHealthJob
		if json.Unmarshal(j.Payload, &p) != nil {
			t.Fatal("payload")
		}
		p.ID = j.ID
		p.Owner = "health-owner"
		p.Token = j.LeaseToken
		return p
	}
	p := claim()
	bad := p
	bad.Token++
	if e := health.ExecuteJob(ctx, bad); e == nil {
		t.Fatal("old token authorized health")
	}
	release, e := postgres.AcquireIndexerPermit(ctx, f.pool, f.idx)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = health.ExecuteJob(ctx, p); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("health bypassed physical lock", e)
	}
	release()
	f.svc.RegisterClient(f.idx, &failingIndexer{})
	if e = health.ExecuteJob(ctx, p); e != nil {
		t.Fatal("failed probe observation not committed", e)
	}
	row, e := f.q.GetIndexerByID(ctx, f.idx)
	if e != nil || row.Health != "offline" || row.HealthReason != "unavailable" || row.HealthFailures != 1 || !row.HealthCheckedAt.Valid || !row.HealthNextCheckAt.Time.After(time.Now().Add(25*time.Second)) {
		t.Fatal("no persisted failure/backoff", row, e)
	}
	if e = repo.CompleteHealth(ctx, p, nil); e == nil {
		t.Fatal("completed health token replay accepted")
	}
	// Periodic work is rebuilt from durable due time; an old expired logical lease
	// never releases a physical query permit. It can retry only after acquire succeeds.
	exec("UPDATE indexer SET health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", f.idx)
	if n, e := health.QueueDue(ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	p = claim()
	exec("UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", p.ID)
	if e = health.ExecuteJob(ctx, p); e == nil {
		t.Fatal("expired lease authorized")
	}
	if _, e = f.q.RecoverSearchJobs(ctx); e != nil {
		t.Fatal(e)
	}
	newOwner := claim()
	if newOwner.Token <= p.Token {
		t.Fatal("claim recovery reused token")
	}
	f.svc.RegisterClient(f.idx, &mockIndexerClient{})
	if e = health.ExecuteJob(ctx, newOwner); e != nil {
		t.Fatal(e)
	}
	row, e = f.q.GetIndexerByID(ctx, f.idx)
	if e != nil || row.Health != "online" || row.HealthFailures != 0 || row.HealthReason != "ok" {
		t.Fatal("recovery without actual successful empty response", row, e)
	}
	// Receipt order is not observation order; delayed older success must not
	// overwrite a newer failure, and a final event fault rolls back health + job.
	first, e := f.svc.ExecuteSearch(ctx, f.idx, "first-observation", 0)
	if e != nil {
		t.Fatal(e)
	}
	f.svc.RegisterClient(f.idx, &failingIndexer{})
	second, e := f.svc.ExecuteSearch(ctx, f.idx, "second-observation", 0)
	if !errors.Is(e, ports.ErrUnavailable) || second == nil {
		t.Fatal("failure lacks immutable query receipt", e)
	}
	if e = repo.ReportManualHealth(ctx, f.idx, row.ConfigVersion, second.QueryID); e != nil {
		t.Fatal(e)
	}
	if e = repo.ReportManualHealth(ctx, f.idx, row.ConfigVersion, first.QueryID); e != nil {
		t.Fatal(e)
	}
	newest, e := f.q.GetIndexerByID(ctx, f.idx)
	if e != nil || newest.Health != "offline" || newest.HealthQueryID.Bytes != second.QueryID {
		t.Fatal("old success recolored newer failure", newest, e)
	}
	f.svc.RegisterClient(f.idx, &mockIndexerClient{})

	exec("UPDATE indexer SET health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", f.idx)
	if n, e := health.QueueDue(ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	p = claim()
	failureID := newest.HealthQueryID.Bytes
	exec("CREATE FUNCTION reject_health_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='indexer.health_observed' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_health_event BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION reject_health_event()")
	if e = health.ExecuteJob(ctx, p); e == nil {
		t.Fatal("health event failure acknowledged")
	}
	rolled, e := f.q.GetIndexerByID(ctx, f.idx)
	if e != nil || rolled.Health != "offline" || rolled.HealthQueryID.Bytes != failureID {
		t.Fatal("health partially committed before event", rolled, e)
	}
	var state string
	if e = f.pool.QueryRow(ctx, "SELECT state FROM job WHERE id=$1", p.ID).Scan(&state); e != nil || state != "running" {
		t.Fatal("job succeeded without health event", state, e)
	}
	exec("DROP TRIGGER reject_health_event ON outbox_event")
	if e = health.ExecuteJob(ctx, p); e != nil {
		t.Fatal(e)
	}
	// A remote 429 is a real deferred observation, not an offline verdict, and
	// both reconstruction and manual requests honor its shared durable budget.
	exec("UPDATE indexer SET health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", f.idx)
	if n, e := health.QueueDue(ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	p = claim()
	f.svc.RegisterClient(f.idx, &rateEndpoint{})
	if e = health.ExecuteJob(ctx, p); e != nil {
		t.Fatal(e)
	}
	rate, e := f.q.GetIndexerByID(ctx, f.idx)
	if e != nil || rate.Health != "online" || rate.HealthReason != "rate_limited" || !rate.NextQueryAt.Time.After(time.Now().Add(50*time.Second)) {
		t.Fatal("429 turned offline or lost retry budget", rate, e)
	}
	if n, e := health.QueueDue(ctx, 20); e != nil || n != 0 {
		t.Fatal("health bypassed RetryAfter", n, e)
	}
	if _, e = f.svc.ExecuteSearch(ctx, f.idx, "manual-during-rate", 0); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("manual bypassed budget", e)
	}
	exec("UPDATE indexer SET next_query_at=clock_timestamp() WHERE id=$1", f.idx)
	f.svc.RegisterClient(f.idx, &mockIndexerClient{})

	// A completed query from the old configuration may not recolor a new endpoint.
	result, e := f.svc.ExecuteSearch(ctx, f.idx, "old-config-query", 0)
	if e != nil {
		t.Fatal(e)
	}
	version := row.ConfigVersion
	exec("UPDATE indexer SET config_version=config_version+1,health='offline' WHERE id=$1", f.idx)
	if e = repo.ReportManualHealth(ctx, f.idx, version, result.QueryID); e == nil {
		t.Fatal("old config callback changed current health")
	}
	row, e = f.q.GetIndexerByID(ctx, f.idx)
	if e != nil || row.Health != "offline" || row.HealthCheckedAt.Valid || row.HealthReason != "not_checked" {
		t.Fatal("config did not clear old observation", row, e)
	}
	_ = uuid.Nil
}
