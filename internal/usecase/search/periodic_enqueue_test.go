package search_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	search "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

type schedulerOnlyHealthProbe struct{ calls atomic.Int32 }

func (p *schedulerOnlyHealthProbe) ProbeIndexer(context.Context, ports.IndexerHealthJob) (*ports.IndexerQueryResult, error) {
	p.calls.Add(1)
	return nil, ports.ErrUnavailable
}

func TestPeriodicSchedulingOnlyEnqueuesAndRollsBack(t *testing.T) {
	for _, kind := range []string{"rss_poll", "indexer_health"} {
		t.Run(kind, func(t *testing.T) {
			f, feed, network := feedFixture(t)
			ctx := context.Background()
			probe := &schedulerOnlyHealthProbe{}
			health := search.NewHealthService(postgres.NewIndexerHealthStore(f.pool, idgen.New()), probe)
			queue := feed.QueueDue
			column := "rss_next_poll_at"
			if kind == "indexer_health" {
				queue = health.QueueDue
				column = "health_next_check_at"
				if _, err := f.pool.Exec(ctx, "UPDATE indexer SET health='offline',health_next_check_at=clock_timestamp(),next_query_at=clock_timestamp() WHERE id=$1", f.idx); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() string {
				t.Helper()
				var value string
				if err := f.pool.QueryRow(ctx, "SELECT to_jsonb(i)::text FROM indexer i WHERE id=$1", f.idx).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := snapshot()
			// Fail the last schedule write after the durable job insertion.
			if _, err := f.pool.Exec(ctx, "CREATE FUNCTION reject_scheduler_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic scheduler write failure'; END $$; CREATE TRIGGER reject_scheduler_write BEFORE UPDATE OF "+column+" ON indexer FOR EACH ROW EXECUTE FUNCTION reject_scheduler_write()"); err != nil {
				t.Fatal(err)
			}
			if n, err := queue(ctx, 20); err == nil || n != 0 {
				t.Fatalf("failed schedule did not reject: n=%d err=%v", n, err)
			}
			if after := snapshot(); after != before {
				t.Fatal("failed schedule mutated indexer")
			}
			var jobs int
			if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind=$1", kind).Scan(&jobs); err != nil || jobs != 0 {
				t.Fatalf("failed schedule left durable job: %d %v", jobs, err)
			}
			if network.calls.Load() != 0 || probe.calls.Load() != 0 {
				t.Fatal("periodic enqueue performed external I/O")
			}
			if _, err := f.pool.Exec(ctx, "DROP TRIGGER reject_scheduler_write ON indexer; DROP FUNCTION reject_scheduler_write()"); err != nil {
				t.Fatal(err)
			}
			if n, err := queue(ctx, 20); err != nil || n != 1 {
				t.Fatalf("normal recovery did not enqueue: %d %v", n, err)
			}
			// Simulate reconstruction of the scheduler without running any consumer.
			if kind == "rss_poll" {
				queue = search.NewFeedService(postgres.NewRSSStore(f.pool, idgen.New()), nil, f.svc).QueueDue
			} else {
				queue = search.NewHealthService(postgres.NewIndexerHealthStore(f.pool, idgen.New()), probe).QueueDue
			}
			if n, err := queue(ctx, 20); err != nil || n != 0 {
				t.Fatalf("scheduler reconstruction duplicated work: %d %v", n, err)
			}
			var untouched int
			if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind=$1 AND state='queued' AND attempts=0 AND lease_token=0 AND lease_owner IS NULL AND lease_until IS NULL", kind).Scan(&untouched); err != nil || untouched != 1 {
				t.Fatalf("enqueue executed or duplicated work: %d %v", untouched, err)
			}
			if network.calls.Load() != 0 || probe.calls.Load() != 0 {
				t.Fatal("reconstructed scheduler performed external I/O")
			}
		})
	}
}
