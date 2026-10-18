package app

import (
	"context"
	"errors"
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

type observationWorkerRuntime struct {
	actor *ports.Actor
	err   error
}

func (*observationWorkerRuntime) ScheduleMovieUpgradeObservations(context.Context) (int, error) {
	return 0, nil
}
func (r *observationWorkerRuntime) MovieUpgradeObservationActor(context.Context, ports.MovieUpgradeObservationJobRequest, uuid.UUID) (*ports.Actor, error) {
	return r.actor, r.err
}

type observationWorkerRepo struct {
	call func(context.Context, *ports.Actor, ports.MovieUpgradeObservationJobRequest) (*ports.MovieUpgradeObservationResult, error)
}

func (r observationWorkerRepo) ObserveMovieUpgradeJob(c context.Context, a *ports.Actor, q ports.MovieUpgradeObservationJobRequest) (*ports.MovieUpgradeObservationResult, error) {
	return r.call(c, a, q)
}

type observationWorkerQueue struct {
	job     db.Job
	claimed atomic.Bool
	failed  atomic.Int32
}

func (q *observationWorkerQueue) Claim(_ context.Context, kinds []string, owner string, _ time.Duration) (*db.Job, error) {
	if len(kinds) != 1 || kinds[0] != ports.MovieUpgradeObservationJobKind {
		return nil, errors.New("wrong kind")
	}
	if q.claimed.Swap(true) {
		return nil, nil
	}
	j := q.job
	j.LeaseOwner = pgtype.Text{String: owner, Valid: true}
	return &j, nil
}
func (q *observationWorkerQueue) Fail(_ context.Context, _ uuid.UUID, _ string, _ int64, message string, _ time.Duration) error {
	if message != "movie upgrade observation requires manual review" {
		return errors.New("unsafe error")
	}
	q.failed.Add(1)
	return nil
}
func observationWorkerFixture() (*App, *observationWorkerQueue, *observationWorkerRuntime) {
	actor := uuid.New()
	payload := fmt.Sprintf(`{"operation_id":%q,"task_id":%q,"actor_id":%q}`, uuid.NewString(), uuid.NewString(), actor.String())
	return &App{idgen: idgen.New(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, &observationWorkerQueue{job: db.Job{ID: uuid.New(), Kind: ports.MovieUpgradeObservationJobKind, State: "running", Payload: []byte(payload), Attempts: 1, MaxAttempts: 1, LeaseOwner: pgtype.Text{String: uuid.NewString(), Valid: true}, LeaseToken: 1, LeaseUntil: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}}}, &observationWorkerRuntime{actor: &ports.Actor{ID: actor.String(), Type: ports.ActorTypeUser, Role: ports.RoleMember}}
}
func TestMovieUpgradeObservationWorkerPayload(t *testing.T) {
	_, q, _ := observationWorkerFixture()
	good := string(q.job.Payload)
	for _, raw := range []string{good[:len(good)-1] + `,"actor_id":"` + uuid.NewString() + `"}`, strings.Replace(good, "operation_id", "Operation_ID", 1), good[:len(good)-1] + `,"url":"secret"}`, `[]`, good + `{}`, strings.Replace(good, `"actor_id"`, `"actor_ıd"`, 1)} {
		if _, _, _, e := movieUpgradeObservationPayload([]byte(raw)); e == nil {
			t.Fatal("accepted malformed payload")
		}
	}
	if _, _, _, e := movieUpgradeObservationPayload(q.job.Payload); e != nil {
		t.Fatal(e)
	}
}
func TestMovieUpgradeObservationWorkerCurrentActor(t *testing.T) {
	a, q, r := observationWorkerFixture()
	r.err = ports.ErrForbidden
	called := false
	a.runMovieUpgradeObservationJob(context.Background(), q, r, observationWorkerRepo{call: func(context.Context, *ports.Actor, ports.MovieUpgradeObservationJobRequest) (*ports.MovieUpgradeObservationResult, error) {
		called = true
		return nil, nil
	}}, &q.job)
	if called || q.failed.Load() != 1 {
		t.Fatal("unauthorized query or missing dead transition")
	}
}
func TestMovieUpgradeObservationWorkerDrainsActualReturn(t *testing.T) {
	a, q, r := observationWorkerFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("worker cleanup did not drain")
		}
	}()
	repo := observationWorkerRepo{call: func(_ context.Context, actor *ports.Actor, req ports.MovieUpgradeObservationJobRequest) (*ports.MovieUpgradeObservationResult, error) {
		if actor != r.actor || req.Lease.Owner == "" {
			t.Error("lost original actor/claim")
		}
		close(entered)
		<-release
		return &ports.MovieUpgradeObservationResult{Disposition: "manual_review"}, nil
	}}
	go func() { defer close(done); a.runMovieUpgradeObservationWorker(ctx, q, r, repo) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not execute")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("released active call")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not drain")
	}
	if q.failed.Load() != 0 {
		t.Fatal("successful result failed again")
	}
}
