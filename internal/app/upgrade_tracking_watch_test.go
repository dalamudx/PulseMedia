package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type movieUpgradeWakeQueue struct {
	base         trackingWorkerQueue
	first        chan struct{}
	ready        <-chan struct{}
	seen         atomic.Bool
	initialError bool
}

func (q *movieUpgradeWakeQueue) Claim(ctx context.Context, kinds []string, owner string, lease time.Duration) (*db.Job, error) {
	if !q.seen.Swap(true) {
		close(q.first)
	}
	select {
	case <-q.ready:
		return q.base.Claim(ctx, kinds, owner, lease)
	default:
	}
	if len(kinds) != 1 || kinds[0] != ports.MovieUpgradeTrackingJobKind {
		panic("watch woke a non-tracking job kind")
	}
	if q.initialError {
		return nil, ports.ErrUnavailable
	}
	return nil, nil
}
func (q *movieUpgradeWakeQueue) Fail(ctx context.Context, id uuid.UUID, owner string, token int64, reason string, delay time.Duration) error {
	return q.base.Fail(ctx, id, owner, token, reason, delay)
}

type movieUpgradeWakeRuntime struct {
	trackingWorkerRuntime
	scans atomic.Int32
}

func (r *movieUpgradeWakeRuntime) ScheduleMovieUpgradeTracking(context.Context) (int, error) {
	r.scans.Add(1)
	return 0, nil
}

func TestMovieUpgradeTrackingWakeOnlyClaims(t *testing.T) {
	for _, initialError := range []bool{false, true} {
		name := "idle"
		if initialError {
			name = "claim_error"
		}
		t.Run(name, func(t *testing.T) {
			id := uuid.New()
			ready := make(chan struct{})
			q := &movieUpgradeWakeQueue{base: trackingWorkerQueue{job: db.Job{ID: id, Payload: []byte(fmt.Sprintf(`{"tracking_job_id":%q}`, id.String())), LeaseToken: 1}}, first: make(chan struct{}), ready: ready, initialError: initialError}
			runtime := &movieUpgradeWakeRuntime{trackingWorkerRuntime: trackingWorkerRuntime{actor: &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}}}
			a := &App{idgen: idgen.New(), clock: trackingWorkerClock{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			wake := make(chan struct{}, 1)
			entered := make(chan struct{}, 1)
			done := make(chan struct{})
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("tracking wake worker did not join")
				}
			})
			repo := trackingWorkerRepo{call: func(_ context.Context, actor *ports.Actor, req ports.MovieUpgradeTrackingJobRequest) (*ports.MovieUpgradeTrackingResult, error) {
				if actor != runtime.actor || req.Lease.ID != id || req.Lease.Owner == "" {
					t.Error("watch wake lost the original tracking claim")
				}
				entered <- struct{}{}
				return &ports.MovieUpgradeTrackingResult{Outcome: "completed", Disposition: "organization_review_required"}, nil
			}}
			go func() { defer close(done); a.runMovieUpgradeTrackingWorker(ctx, q, runtime, repo, wake) }()
			select {
			case <-q.first:
			case <-time.After(time.Second):
				t.Fatal("initial periodic pass did not finish")
			}
			close(ready)
			wake <- struct{}{}
			select {
			case <-entered:
			case <-time.After(800 * time.Millisecond):
				t.Fatal("tracking hint waited for the two-second claim ticker")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("worker did not stop")
			}
			if runtime.scans.Load() != 1 || runtime.calls.Load() != 1 || q.base.failed.Load() != 0 {
				t.Fatal("hint restarted global scheduling or lost the single tracking claim")
			}
		})
	}
}
