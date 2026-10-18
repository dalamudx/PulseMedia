package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

func TestJobCurrentAuthorityAndTerminalState(t *testing.T) {
	for _, action := range []string{"finish", "raw_finish", "fail", "renew", "submission_guard"} {
		t.Run(action, func(t *testing.T) {
			queue, dsn := setupJobsTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			q := postgres.Queries(pool)
			if _, err := queue.Enqueue(ctx, "download_submission", "", 0, nil, time.Time{}, 3); err != nil {
				t.Fatal(err)
			}
			job, err := queue.Claim(ctx, []string{"download_submission"}, "current-owner", 20*time.Second)
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
			apply := func(owner string, token int64) error {
				switch action {
				case "finish":
					return queue.Finish(ctx, job.ID, owner, token, map[string]bool{"done": true})
				case "raw_finish":
					_, err := q.FinishJob(ctx, db.FinishJobParams{Column1: job.ID, Column2: owner, Column3: token, Column4: []byte(`{"done":true}`)})
					return err
				case "fail":
					return queue.Fail(ctx, job.ID, owner, token, "synthetic failure", time.Second)
				case "renew":
					return queue.RenewLease(ctx, job.ID, owner, token, 20*time.Second)
				default:
					_, err := q.GuardDownloadSubmissionCommit(ctx, db.GuardDownloadSubmissionCommitParams{ID: job.ID, Owner: owner, Token: token})
					return err
				}
			}
			expected := jobs.ErrStaleLease
			if action == "raw_finish" || action == "submission_guard" {
				expected = pgx.ErrNoRows
			}
			for _, wrong := range []struct {
				owner string
				token int64
			}{{"other-owner", job.LeaseToken}, {"current-owner", job.LeaseToken + 1}} {
				before := snapshot()
				if err := apply(wrong.owner, wrong.token); !errors.Is(err, expected) {
					t.Fatalf("invalid authority accepted: %v", err)
				}
				if snapshot() != before {
					t.Fatal("invalid authority changed row")
				}
			}
			if err := apply("current-owner", job.LeaseToken); err != nil {
				t.Fatalf("valid authority rejected: %v", err)
			}
			row, err := q.GetJobByID(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "finish", "raw_finish":
				if row.State != "succeeded" || string(row.Result) != "{\"done\": true}" {
					t.Fatal("completion result not saved")
				}
			case "fail":
				if row.State != "queued" || row.LeaseOwner.Valid || row.Error.String != "synthetic failure" {
					t.Fatal("retry transition not saved")
				}
			case "renew", "submission_guard":
				if row.State != "running" || row.LeaseToken != job.LeaseToken {
					t.Fatal("live authority changed")
				}
				if err := queue.Finish(ctx, job.ID, "current-owner", job.LeaseToken, nil); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot()
			if err := apply("current-owner", job.LeaseToken); !errors.Is(err, expected) {
				t.Fatalf("inactive job accepted write: %v", err)
			}
			if snapshot() != before {
				t.Fatal("inactive state changed")
			}
		})
	}
}

func TestJobRetryReclaimRejectsPriorTokenAndReachesDead(t *testing.T) {
	queue, dsn := setupJobsTest(t)
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := postgres.Queries(pool)
	if _, err := queue.Enqueue(ctx, "probe", "", 0, nil, time.Time{}, 2); err != nil {
		t.Fatal(err)
	}
	first, err := queue.Claim(ctx, []string{"probe"}, "same-owner", 10*time.Second)
	if err != nil || first == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := queue.Fail(ctx, first.ID, "same-owner", first.LeaseToken, "first failure", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE job SET not_before=clock_timestamp() WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	next, err := queue.Claim(ctx, []string{"probe"}, "same-owner", 10*time.Second)
	if err != nil || next == nil {
		t.Fatalf("retry claim: %v", err)
	}
	if next.LeaseToken <= first.LeaseToken || next.Attempts != 2 {
		t.Fatal("retry did not advance token and attempts")
	}
	if err := queue.Fail(ctx, first.ID, "same-owner", first.LeaseToken, "obsolete failure", time.Second); !errors.Is(err, jobs.ErrStaleLease) {
		t.Fatalf("prior owner token accepted: %v", err)
	}
	if err := queue.Fail(ctx, next.ID, "same-owner", next.LeaseToken, "last failure", time.Second); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetJobByID(ctx, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "dead" || row.LeaseOwner.Valid || row.LeaseUntil.Valid || row.Error.String != "last failure" {
		t.Fatal("dead state not saved")
	}
	if claimed, err := queue.Claim(ctx, []string{"probe"}, "third-owner", time.Second); err != nil || claimed != nil {
		t.Fatalf("dead job reclaimed: %v", err)
	}
}
