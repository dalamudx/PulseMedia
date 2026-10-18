package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

const migrationsDir = "../../../migrations"

func setupJobsTest(t *testing.T) (*jobs.Queue, string) {
	t.Helper()
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
	})

	gen := idgen.New()
	c := clock.New()
	q := jobs.NewQueue(pool, gen, c)
	return q, dbDSN
}

func TestConcurrentSkipLockedClaim(t *testing.T) {
	q, _ := setupJobsTest(t)
	ctx := context.Background()

	// 1. Enqueue 2 jobs
	job1, err := q.Enqueue(ctx, "download", "task_001", 10, map[string]string{"file": "movie1.mkv"}, time.Time{}, 3)
	if err != nil {
		t.Fatalf("failed to enqueue job 1: %v", err)
	}
	job2, err := q.Enqueue(ctx, "download", "task_002", 5, map[string]string{"file": "movie2.mkv"}, time.Time{}, 3)
	if err != nil {
		t.Fatalf("failed to enqueue job 2: %v", err)
	}

	// 2. Concurrently claim with two workers
	var wg sync.WaitGroup
	var err1, err2 error
	var c1Job, c2Job *db.Job

	wg.Add(2)
	go func() {
		defer wg.Done()
		j, err := q.Claim(ctx, []string{"download"}, "worker_1", 10*time.Second)
		c1Job = j
		err1 = err
	}()
	go func() {
		defer wg.Done()
		j, err := q.Claim(ctx, []string{"download"}, "worker_2", 10*time.Second)
		c2Job = j
		err2 = err
	}()
	wg.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("claim errors: w1=%v, w2=%v", err1, err2)
	}
	if c1Job == nil || c2Job == nil {
		t.Fatalf("both workers must claim a job: w1=%v, w2=%v", c1Job, c2Job)
	}

	// Two claimed jobs must be distinct
	if c1Job.ID == c2Job.ID {
		t.Fatalf("expected distinct claimed jobs, both got: %s", c1Job.ID)
	}

	// Verify the claimed jobs are indeed job1 and job2
	claimedIDs := map[string]bool{
		c1Job.ID.String(): true,
		c2Job.ID.String(): true,
	}
	if !claimedIDs[job1.ID.String()] || !claimedIDs[job2.ID.String()] {
		t.Fatalf("claimed jobs do not match enqueued jobs: claimed=%+v, expected [%s, %s]",
			claimedIDs, job1.ID, job2.ID)
	}

	// 3. Third claim returns nil (no more ready jobs)
	third, err := q.Claim(ctx, []string{"download"}, "worker_3", 10*time.Second)
	if err != nil || third != nil {
		t.Fatalf("expected nil for 3rd claim, got: %v (err: %v)", third, err)
	}

	// Finish both jobs
	if err := q.Finish(ctx, c1Job.ID, c1Job.LeaseOwner.String, c1Job.LeaseToken, map[string]string{"ok": "true"}); err != nil {
		t.Fatalf("failed to finish job 1: %v", err)
	}
	if err := q.Finish(ctx, c2Job.ID, c2Job.LeaseOwner.String, c2Job.LeaseToken, map[string]string{"ok": "true"}); err != nil {
		t.Fatalf("failed to finish job 2: %v", err)
	}
}

func TestStaleLeaseWritebackRejected(t *testing.T) {
	q, _ := setupJobsTest(t)
	ctx := context.Background()

	// 1. Enqueue job
	j, err := q.Enqueue(ctx, "scan", "scan_001", 0, "payload", time.Time{}, 3)
	if err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	// 2. Claim with very short lease (10ms)
	claimed, err := q.Claim(ctx, []string{"scan"}, "worker_slow", 10*time.Millisecond)
	if err != nil || claimed == nil {
		t.Fatalf("failed to claim: %v", err)
	}

	// 3. Sleep beyond lease duration
	time.Sleep(50 * time.Millisecond)

	// 4. Worker attempts to write back completion: must fail with ErrStaleLease!
	err = q.Finish(ctx, claimed.ID, "worker_slow", claimed.LeaseToken, map[string]string{"status": "done"})
	if !errors.Is(err, jobs.ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for expired lease, got: %v", err)
	}

	// 5. Wrong lease token also rejected
	err = q.Finish(ctx, claimed.ID, "worker_slow", 99999, map[string]string{"status": "done"})
	if !errors.Is(err, jobs.ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for wrong token, got: %v", err)
	}

	// 6. Wrong owner also rejected
	err = q.Finish(ctx, claimed.ID, "wrong_worker", claimed.LeaseToken, map[string]string{"status": "done"})
	if !errors.Is(err, jobs.ErrStaleLease) {
		t.Fatalf("expected ErrStaleLease for wrong owner, got: %v", err)
	}
	_ = j
}
