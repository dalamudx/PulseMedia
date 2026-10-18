package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func TestJobAgedPriorityCannotBeStarved(t *testing.T) {
	queue, dsn := setupJobsTest(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var now time.Time
	if err = pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	low, err := queue.Enqueue(ctx, "fairness", "", -2147483648, nil, now.Add(-6*time.Minute), 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err = queue.Enqueue(ctx, "fairness", "", 2147483647, nil, now, 3); err != nil {
			t.Fatal(err)
		}
		claimed, e := queue.Claim(ctx, []string{"fairness"}, "fair-worker", time.Minute)
		if e != nil || claimed == nil {
			t.Fatalf("claim: %v", e)
		}
		if claimed.ID == low.ID {
			if claimed.Priority != low.Priority {
				t.Fatal("fairness rewrote immutable priority")
			}
			return
		}
		if e = queue.Finish(ctx, claimed.ID, "fair-worker", claimed.LeaseToken, nil); e != nil {
			t.Fatal(e)
		}
	}
	t.Fatal("old eligible low-priority job starved under continuous fresh high-priority arrivals")
}

func TestJobFairnessRetainsPriorityReadinessAndSkipLocked(t *testing.T) {
	queue, dsn := setupJobsTest(t)
	ctx := context.Background()
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
		job, e := queue.Enqueue(ctx, "fairness_controls", "", priority, nil, ready, 3)
		if e != nil {
			t.Fatal(e)
		}
		return job
	}
	low := enqueue(-10, now)
	high := enqueue(10, now)
	future := enqueue(2147483647, now.Add(time.Hour))
	old := enqueue(-2147483648, now.Add(-7*time.Minute))
	newerOld := enqueue(2147483647, now.Add(-6*time.Minute))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SELECT id FROM job WHERE id=$1 FOR UPDATE", old.ID); err != nil {
		t.Fatal(err)
	}
	claim := func(expected uuid.UUID) {
		t.Helper()
		job, e := queue.Claim(ctx, []string{"fairness_controls"}, "controls", time.Minute)
		if e != nil || job == nil || job.ID != expected {
			t.Fatalf("expected %s, got %+v: %v", expected, job, e)
		}
		if e = queue.Finish(ctx, job.ID, "controls", job.LeaseToken, nil); e != nil {
			t.Fatal(e)
		}
	}
	claim(newerOld.ID)
	claim(high.ID)
	claim(low.ID)
	job, err := queue.Claim(ctx, []string{"fairness_controls"}, "controls", time.Minute)
	if err != nil || job != nil {
		t.Fatalf("locked and future jobs must not be claimed: %+v %v", job, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	claim(old.ID)
	var state string
	var attempts int32
	var token int64
	err = pool.QueryRow(ctx, "SELECT state,attempts,lease_token FROM job WHERE id=$1", future.ID).Scan(&state, &attempts, &token)
	if err != nil || state != "queued" || attempts != 0 || token != 0 {
		t.Fatalf("future job changed: state=%s attempts=%d token=%d err=%v", state, attempts, token, err)
	}
}
