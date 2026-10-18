package app

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type trackingWorkerClock struct{}

func (trackingWorkerClock) Now() time.Time                  { return time.Now() }
func (trackingWorkerClock) Since(t time.Time) time.Duration { return time.Since(t) }

type trackingWorkerRuntime struct {
	actor *ports.Actor
	err   error
	calls atomic.Int32
}

func (r *trackingWorkerRuntime) ScheduleMovieUpgradeTracking(context.Context) (int, error) {
	return 0, nil
}
func (r *trackingWorkerRuntime) MovieUpgradeTrackingClaim(_ context.Context, l ports.JobLease) (*ports.Actor, ports.MovieUpgradeTrackingJobRequest, error) {
	r.calls.Add(1)
	return r.actor, ports.MovieUpgradeTrackingJobRequest{Lease: l, OperationID: uuid.New(), TaskID: uuid.New()}, r.err
}

type trackingWorkerRepo struct {
	call func(context.Context, *ports.Actor, ports.MovieUpgradeTrackingJobRequest) (*ports.MovieUpgradeTrackingResult, error)
}

func (r trackingWorkerRepo) TrackMovieUpgradeJob(c context.Context, a *ports.Actor, q ports.MovieUpgradeTrackingJobRequest) (*ports.MovieUpgradeTrackingResult, error) {
	return r.call(c, a, q)
}

type trackingWorkerQueue struct {
	job     db.Job
	claimed atomic.Bool
	failed  atomic.Int32
}

func (q *trackingWorkerQueue) Claim(_ context.Context, kinds []string, owner string, _ time.Duration) (*db.Job, error) {
	if len(kinds) != 1 || kinds[0] != ports.MovieUpgradeTrackingJobKind {
		panic("wrong kind")
	}
	if q.claimed.Swap(true) {
		return nil, nil
	}
	j := q.job
	j.LeaseOwner = pgtype.Text{String: owner, Valid: true}
	return &j, nil
}
func (q *trackingWorkerQueue) Fail(_ context.Context, _ uuid.UUID, _ string, _ int64, reason string, _ time.Duration) error {
	if reason != "upgrade_tracking_manual_review" && reason != "upgrade_tracking_invalid_payload" {
		panic("unsafe reason")
	}
	q.failed.Add(1)
	return nil
}
func TestMovieUpgradeTrackingWorkerPayload(t *testing.T) {
	id := uuid.New()
	good := fmt.Sprintf(`{"tracking_job_id":%q}`, id.String())
	if got, e := movieUpgradeTrackingPayload([]byte(good)); e != nil || got != id {
		t.Fatal(got, e)
	}
	for _, raw := range []string{good[:len(good)-1] + `,"tracking_job_id":"` + id.String() + `"}`, strings.Replace(good, "tracking_job_id", "Tracking_Job_ID", 1), strings.Replace(good, id.String(), strings.ToUpper(id.String()), 1), `{"tracking_job_id":null}`, `{"tracking_job_id":"00000000-0000-0000-0000-000000000000"}`, good[:len(good)-1] + `,"url":"not-allowed"}`, good + `{}`, `[]`} {
		if _, e := movieUpgradeTrackingPayload([]byte(raw)); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestMovieUpgradeTrackingWorkerDrainsActualReturn(t *testing.T) {
	id := uuid.New()
	q := &trackingWorkerQueue{job: db.Job{ID: id, Payload: []byte(fmt.Sprintf(`{"tracking_job_id":%q}`, id.String())), LeaseToken: 1}}
	runtime := &trackingWorkerRuntime{actor: &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleMember}}
	a := &App{idgen: idgen.New(), clock: trackingWorkerClock{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("did not drain")
		}
	}()
	repo := trackingWorkerRepo{call: func(_ context.Context, actor *ports.Actor, req ports.MovieUpgradeTrackingJobRequest) (*ports.MovieUpgradeTrackingResult, error) {
		if actor != runtime.actor || req.Lease.ID != id || req.Lease.Owner == "" {
			t.Error("lost actor/claim")
		}
		close(entered)
		<-release
		return &ports.MovieUpgradeTrackingResult{Outcome: "completed", Disposition: "organization_review_required"}, nil
	}}
	go func() { defer close(done); a.runMovieUpgradeTrackingWorker(ctx, q, runtime, repo) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not entered")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("abandoned active call")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("did not finish")
	}
	if q.failed.Load() != 0 {
		t.Fatal("extra failure")
	}
}
