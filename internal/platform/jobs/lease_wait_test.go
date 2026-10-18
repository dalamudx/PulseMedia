package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The blocker never changes the tuple: PostgreSQL must not rely on an updated
// tuple causing a predicate recheck after the lease expires while waiting.
func TestJobLeaseExpiryDuringRowWait(t *testing.T) {
	for _, action := range []string{"finish", "fail", "renew", "submission_guard"} {
		t.Run(action, func(t *testing.T) {
			_, dsn := setupJobsTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cfg, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
				_, err := conn.Exec(ctx, "SET lock_timeout='10s'; SET statement_timeout='15s'")
				return err
			}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			queue := jobs.NewQueue(pool, idgen.New(), clock.New())
			q := postgres.Queries(pool)
			if _, err := queue.Enqueue(ctx, "download_submission", "", 0, nil, time.Time{}, 3); err != nil {
				t.Fatal(err)
			}
			job, err := queue.Claim(ctx, []string{"download_submission"}, "wait-owner", 2*time.Second)
			if err != nil || job == nil {
				t.Fatalf("claim: %v", err)
			}
			var before string
			if err := pool.QueryRow(ctx, "SELECT to_jsonb(j)::text FROM job j WHERE id=$1", job.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			var blocker int32
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid() FROM job WHERE id=$1 FOR UPDATE", job.ID).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				var err error
				switch action {
				case "finish":
					err = queue.Finish(ctx, job.ID, "wait-owner", job.LeaseToken, map[string]bool{"done": true})
				case "fail":
					err = queue.Fail(ctx, job.ID, "wait-owner", job.LeaseToken, "synthetic failure", time.Second)
				case "renew":
					err = queue.RenewLease(ctx, job.ID, "wait-owner", job.LeaseToken, 10*time.Second)
				case "submission_guard":
					_, err = q.GuardDownloadSubmissionCommit(ctx, db.GuardDownloadSubmissionCommitParams{ID: job.ID, Owner: "wait-owner", Token: job.LeaseToken})
				}
				result <- err
			}()
			defer func() {
				cancel()
				_ = tx.Rollback(context.WithoutCancel(ctx))
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
					t.Error("lease operation did not stop")
				}
			}()
			for {
				var blocked bool
				if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::int=ANY(pg_blocking_pids(pid)))", blocker).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("operation did not wait on row lock: %v", err)
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			var valid bool
			if err := pool.QueryRow(ctx, "SELECT clock_timestamp() < $1::timestamptz", job.LeaseUntil.Time).Scan(&valid); err != nil {
				t.Fatal(err)
			}
			if !valid {
				t.Fatal("lease expired before blocked operation was observed")
			}
			for valid {
				time.Sleep(10 * time.Millisecond)
				if err := pool.QueryRow(ctx, "SELECT clock_timestamp() < $1::timestamptz", job.LeaseUntil.Time).Scan(&valid); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-result
			expected := jobs.ErrStaleLease
			if action == "submission_guard" {
				expected = pgx.ErrNoRows
			}
			if !errors.Is(err, expected) {
				t.Errorf("expired lease accepted after unchanged row lock wait: %v", err)
			}
			var after string
			if err := pool.QueryRow(ctx, "SELECT to_jsonb(j)::text FROM job j WHERE id=$1", job.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Error("expired owner changed job, result or lease")
			}
		})
	}
}
