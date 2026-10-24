package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type seedingOwnedExecutor func(context.Context, ports.JobLease) error

func (f seedingOwnedExecutor) Execute(c context.Context, l ports.JobLease) error { return f(c, l) }

func TestM10SeedingWorkerCancellationWaitsPhysicalPG(t *testing.T) {
	runM10SeedingScheduling(t, func(t *testing.T, f *organizationFixture, task, job, client uuid.UUID) {
		q := db.New(f.pool)
		repo := postgres.NewSeedingExecutionStore(f.pool, idgen.New())
		entered := make(chan ports.JobLease, 1)
		failures := make(chan error, 1)
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		service := seedingOwnedExecutor(func(c context.Context, l ports.JobLease) error {
			p, e := repo.Acquire(c, l)
			if e != nil {
				failures <- e
				return e
			}
			defer p.Close()
			if e = p.Reserve(c); e != nil {
				failures <- e
				return e
			}
			entered <- l
			// A non-cooperative physical call must keep exclusion until it returns.
			<-release
			if e = p.Check(c); !errors.Is(e, context.Canceled) {
				failures <- errors.New("cancelled execution retained logical authorization")
			}
			return c.Err()
		})
		a := &App{idgen: idgen.New(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			a.runSeedingWorker(ctx, jobs.NewQueue(f.pool, idgen.New(), clock.New()), postgres.NewSeedingScheduleStore(f.pool, idgen.New()), service)
		}()
		defer func() {
			cancel()
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("owned worker did not join")
			}
		}()
		var lease ports.JobLease
		select {
		case lease = <-entered:
		case e := <-failures:
			t.Fatal(e)
		case <-time.After(5 * time.Second):
			t.Fatal("worker never acquired physical execution")
		}
		if lease.ID != job {
			t.Fatal("worker changed original job")
		}
		cancel()
		if _, e := f.pool.Exec(t.Context(), `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND lease_token=$2`, job, lease.Token); e != nil {
			t.Fatal(e)
		}
		if e := postgres.NewSeedingScheduleStore(f.pool, idgen.New()).RecoverExpired(t.Context()); e != nil {
			t.Fatal(e)
		}
		current, e := q.GetJobByID(t.Context(), job)
		if e != nil || current.State != "running" || current.LeaseToken != lease.Token {
			t.Fatal("logical expiry released active physical execution", e)
		}
		if p, e := repo.Acquire(t.Context(), lease); e == nil {
			p.Close()
			t.Fatal("second physical execution acquired")
		}
		latest, e := q.LatestSeedingExecution(t.Context(), job)
		if e != nil || latest.ReleasedAt.Valid || latest.Result.Valid {
			t.Fatal("active call marked released", e)
		}
		select {
		case <-done:
			t.Fatal("cancel returned before physical call")
		case <-time.After(50 * time.Millisecond):
		}
		unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled worker did not join after I/O")
		}
		select {
		case e := <-failures:
			t.Fatal(e)
		default:
		}
		latest, e = q.LatestSeedingExecution(t.Context(), job)
		if e != nil || !latest.ReleasedAt.Valid || latest.Result.Valid {
			t.Fatal("physical return missing release evidence", e)
		}
		var permits int
		if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM seeding_removal_permit WHERE task_id=$1`, task).Scan(&permits); e != nil || permits != 0 || f.seedingProbe.removes.Load() != 0 {
			t.Fatal("cancel authorized removal", e)
		}
		t.Log("M10_SEEDING_WORKER_LIFECYCLE_PG_PASS queue=REAL physical_gate=REAL noncooperative_IO=SIMULATED cancellation=WAITED expired_lease=EXCLUSIVE release=AFTER_IO permits=ZERO remove=ZERO")
	})
}
