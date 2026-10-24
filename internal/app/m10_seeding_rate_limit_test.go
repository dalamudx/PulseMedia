package app

import (
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestM10SeedingPrePermitRateLimitNativePG(t *testing.T) {
	runM10SeedingScheduling(t, func(t *testing.T, f *organizationFixture, task, job, _ uuid.UUID) {
		q := db.New(f.pool)
		claimed, err := q.ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"seeding_removal"}, Column2: "seeding-rate-limit", Column3: 120000})
		if err != nil || claimed.ID != job {
			t.Fatal("claim original task", err)
		}
		lease := ports.JobLease{ID: job, Owner: claimed.LeaseOwner.String, Token: claimed.LeaseToken}
		p, err := postgres.NewSeedingExecutionStore(f.pool, idgen.New()).Acquire(t.Context(), lease)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if err = p.Reserve(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = p.Finish(t.Context(), ports.SeedingExecutionFailed, ports.NewRateLimitError(24*time.Hour, "owned detail must not enter ledger")); err != nil {
			t.Fatal(err)
		}
		p.Close()
		var bounded bool
		if err = f.pool.QueryRow(t.Context(), `SELECT j.state='dead' AND j.error='seeding native observation unavailable' AND j.not_before>=e.finished_at+interval '1 hour' AND j.not_before<e.finished_at+interval '3610 seconds' AND s.state='idle' AND s.active_job_id IS NULL AND s.next_due_at>=e.finished_at+interval '1 hour' AND s.next_due_at<e.finished_at+interval '3610 seconds' AND e.released_at IS NOT NULL AND e.requests=1 FROM job j JOIN seeding_removal_execution e ON e.job_id=j.id AND e.lease_token=j.lease_token JOIN seeding_removal_schedule s ON s.task_id=$2 WHERE j.id=$1`, job, task).Scan(&bounded); err != nil || !bounded {
			t.Fatal("rate-limited next round not deferred within one-hour cap", err)
		}
		if _, err = q.GetSeedingRemovalPermit(t.Context(), task); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("pre-permit rate limit authorized removal", err)
		}
		if err = postgres.NewSeedingScheduleStore(f.pool, idgen.New()).EnqueueDue(t.Context()); err != nil {
			t.Fatal(err)
		}
		var jobs int
		if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM job WHERE kind='seeding_removal'`).Scan(&jobs); err != nil || jobs != 1 {
			t.Fatal("rate limit immediately queued another task", err)
		}
		t.Log("M10_SEEDING_RATE_LIMIT_PG_PASS native_IO=NOT_EXECUTED before_permit=NO_PERMISSION next_round=DEFERRED cap=ONE_HOUR errors=SAFE jobs=ONE")
	})
}
