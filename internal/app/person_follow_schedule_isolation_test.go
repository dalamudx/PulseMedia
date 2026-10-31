package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type personScheduleFailureRepository struct {
	ports.PersonFollowSyncRepository
	recoveryError error
	enqueueCalls  int
	cancelEnqueue context.CancelFunc
}

func (r *personScheduleFailureRepository) RecoverExpired(context.Context) error {
	return r.recoveryError
}
func (r *personScheduleFailureRepository) EnqueueDue(context.Context) error {
	r.enqueueCalls++
	if r.cancelEnqueue != nil {
		r.cancelEnqueue()
	}
	return ports.ErrUnavailable
}

type personScheduleExecutor func(context.Context, ports.JobLease) error

func (f personScheduleExecutor) Execute(ctx context.Context, lease ports.JobLease) error {
	return f(ctx, lease)
}

func TestPersonFollowWorkerConsumesDespiteSchedulingFailurePG(t *testing.T) {
	for _, mode := range []string{"scheduling_failure", "recovery_failure", "cancelled_schedule"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationSetup(t)
			queue := jobs.NewQueue(f.pool, f.gen, clock.New())
			job, err := queue.Enqueue(t.Context(), ports.PersonFollowSyncJobKind, "", 0, map[string]any{}, time.Time{}, 3)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			repo := &personScheduleFailureRepository{}
			if mode == "recovery_failure" {
				repo.recoveryError = ports.ErrUnavailable
			}
			if mode == "cancelled_schedule" {
				repo.cancelEnqueue = cancel
			}
			executions := 0
			executor := personScheduleExecutor(func(_ context.Context, lease ports.JobLease) error {
				executions++
				if lease.ID != job.ID || lease.Token <= 0 || lease.Owner == "" {
					t.Error("missing persisted execution authority")
				}
				cancel()
				return nil
			})
			a := &App{dbPool: f.pool, idgen: f.gen, logger: logging.NewLogger(io.Discard, "error")}
			a.runPersonFollowWorker(ctx, queue, repo, executor)
			want := 0
			if mode == "scheduling_failure" {
				want = 1
			}
			if executions != want {
				t.Fatalf("executions=%d want=%d", executions, want)
			}
			if mode == "recovery_failure" && repo.enqueueCalls != 0 {
				t.Fatal("recovery failure allowed scheduling")
			}
			var state string
			var attempts int
			if err = f.pool.QueryRow(t.Context(), `SELECT state,attempts FROM job WHERE id=$1`, job.ID).Scan(&state, &attempts); err != nil {
				t.Fatal(err)
			}
			// The executor cancels after claiming; normal interruption writeback requeues it.
			if state != "queued" || attempts != want {
				t.Fatal("unexpected claim", state, attempts)
			}
		})
	}
}
