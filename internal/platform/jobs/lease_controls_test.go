package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func TestRenewedLeaseDurationStartsAfterRowWait(t *testing.T) {
	queue, dsn := setupJobsTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := queue.Enqueue(ctx, "probe", "", 0, nil, time.Time{}, 3); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Claim(ctx, []string{"probe"}, "live-owner", 10*time.Second)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var pid int32
	if err := tx.QueryRow(ctx, "SELECT pg_backend_pid() FROM job WHERE id=$1 FOR UPDATE", job.ID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		result <- queue.RenewLease(ctx, job.ID, "live-owner", job.LeaseToken, 3*time.Second)
	}()
	defer func() {
		cancel()
		_ = tx.Rollback(context.WithoutCancel(ctx))
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("renewal did not stop")
		}
	}()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::int=ANY(pg_blocking_pids(pid)))", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("renewal did not wait: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(350 * time.Millisecond)
	var released time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&released); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	row, err := postgres.Queries(pool).GetJobByID(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.LeaseUntil.Time.Sub(released) < 2900*time.Millisecond {
		t.Fatal("renewal duration was consumed while waiting for row lock")
	}
	if row.State != "running" || row.LeaseToken != job.LeaseToken || row.LeaseOwner.String != "live-owner" {
		t.Fatal("valid renewal changed authority")
	}
}

func TestJobLateWriteFailureRollsBackAndReleasesLock(t *testing.T) {
	for _, action := range []string{"finish", "fail", "renew"} {
		t.Run(action, func(t *testing.T) {
			queue, dsn := setupJobsTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if _, err := queue.Enqueue(ctx, "probe", "", 0, nil, time.Time{}, 3); err != nil {
				t.Fatal(err)
			}
			job, err := queue.Claim(ctx, []string{"probe"}, "live-owner", 20*time.Second)
			if err != nil || job == nil {
				t.Fatalf("claim: %v", err)
			}
			snapshot := func() string {
				var s string
				if err := pool.QueryRow(ctx, "SELECT to_jsonb(j)::text FROM job j WHERE id=$1", job.ID).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			before := snapshot()
			if _, err := pool.Exec(ctx, "CREATE FUNCTION reject_job_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic late job write failure'; END $$; CREATE TRIGGER reject_job_write AFTER UPDATE ON job FOR EACH ROW EXECUTE FUNCTION reject_job_write()"); err != nil {
				t.Fatal(err)
			}
			apply := func() error {
				switch action {
				case "finish":
					return queue.Finish(ctx, job.ID, "live-owner", job.LeaseToken, map[string]bool{"done": true})
				case "fail":
					return queue.Fail(ctx, job.ID, "live-owner", job.LeaseToken, "synthetic failure", time.Second)
				default:
					return queue.RenewLease(ctx, job.ID, "live-owner", job.LeaseToken, 20*time.Second)
				}
			}
			if err := apply(); err == nil {
				t.Fatal("late write failure accepted")
			}
			if snapshot() != before {
				t.Fatal("late failure left partial state")
			}
			if _, err := pool.Exec(ctx, "DROP TRIGGER reject_job_write ON job; DROP FUNCTION reject_job_write()"); err != nil {
				t.Fatal(err)
			}
			if err := apply(); err != nil {
				t.Fatalf("clean recovery failed: %v", err)
			}
		})
	}
}
