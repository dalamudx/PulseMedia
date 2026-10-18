package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// Reproduce a ready backlog and fresh high-priority arrivals before every claim.
// This measures bounded priority starvation; physical I/O scheduling is separate.
func TestJobFairnessWithBacklogAndSustainedArrivals(t *testing.T) {
	queue, dsn := setupJobsTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var now time.Time
	if err = pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	enqueue := func(priority int32, ready time.Time) *db.Job {
		t.Helper()
		job, e := queue.Enqueue(ctx, "m4_fairness_scale", "", priority, nil, ready, 3)
		if e != nil {
			t.Fatal(e)
		}
		return job
	}
	const agedCount, initialFresh, arrivalsPerClaim = 33, 1024, 64
	const lowPriority, highPriority int32 = -2147483648, 2147483647
	aged := make([]*db.Job, agedCount)
	for n := range aged {
		aged[n] = enqueue(lowPriority, now.Add(-6*time.Minute).Add(time.Duration(n)*time.Millisecond))
	}
	for n := 0; n < initialFresh; n++ {
		enqueue(highPriority, now)
	}
	future := enqueue(highPriority, now.Add(time.Hour))
	locked, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locked.Rollback(context.Background()) }()
	if _, err = locked.Exec(ctx, "SELECT id FROM job WHERE id=$1 FOR UPDATE", aged[0].ID); err != nil {
		t.Fatal(err)
	}
	claim := func(expected *db.Job) {
		t.Helper()
		job, e := queue.Claim(ctx, []string{"m4_fairness_scale"}, "m4-fairness-worker", time.Minute)
		if e != nil || job == nil {
			t.Fatalf("claim returned no job: %v", e)
		}
		if job.ID != expected.ID || job.Priority != lowPriority || job.LeaseOwner.String != "m4-fairness-worker" || job.LeaseToken != 1 || job.Attempts != 1 {
			t.Fatalf("aged ready order/identity changed: got=%s expected=%s priority=%d token=%d attempts=%d", job.ID, expected.ID, job.Priority, job.LeaseToken, job.Attempts)
		}
		if e = queue.Finish(ctx, job.ID, "m4-fairness-worker", job.LeaseToken, nil); e != nil {
			t.Fatal(e)
		}
	}
	for n := 1; n < agedCount; n++ {
		for arrival := 0; arrival < arrivalsPerClaim; arrival++ {
			enqueue(highPriority, now)
		}
		claim(aged[n])
	}
	if err = locked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claim(aged[0])
	var remaining, futureUntouched, completed, prioritiesUnchanged int
	err = pool.QueryRow(ctx, `SELECT
  count(*) FILTER (WHERE state='queued' AND not_before<=$1),
  count(*) FILTER (WHERE id=$2 AND state='queued' AND attempts=0 AND lease_token=0),
  count(*) FILTER (WHERE state='succeeded' AND priority=$3),
  count(*) FILTER (WHERE priority IN ($3,$4))
 FROM job WHERE kind='m4_fairness_scale'`, now, future.ID, lowPriority, highPriority).Scan(&remaining, &futureUntouched, &completed, &prioritiesUnchanged)
	if err != nil {
		t.Fatal(err)
	}
	const freshCount = initialFresh + (agedCount-1)*arrivalsPerClaim
	if remaining != freshCount || futureUntouched != 1 || completed != agedCount || prioritiesUnchanged != freshCount+agedCount+1 {
		t.Fatalf("queued=%d future=%d completed=%d priorities=%d", remaining, futureUntouched, completed, prioritiesUnchanged)
	}
	t.Logf("M4_FAIRNESS_BACKLOG_PASSED initial_fresh=%d arrivals_per_claim=%d arrival_rounds=%d fresh_remaining=%d aged_completed=%d locked_oldest_skipped_then_resumed=true future_untouched=true priorities_unchanged=true", initialFresh, arrivalsPerClaim, agedCount-1, remaining, completed)
}
