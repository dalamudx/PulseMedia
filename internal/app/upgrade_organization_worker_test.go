package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type organizationWorkerRuntime struct {
	actor   *ports.Actor
	pending []ports.MovieUpgradeOrganizationIngestionWork
	calls   atomic.Int32
}

func (r *organizationWorkerRuntime) MovieUpgradeOrganizationClaim(context.Context, ports.JobLease) (*ports.Actor, error) {
	r.calls.Add(1)
	return r.actor, nil
}
func (r *organizationWorkerRuntime) PendingMovieUpgradeOrganizationIngestion(context.Context) ([]ports.MovieUpgradeOrganizationIngestionWork, error) {
	return r.pending, nil
}

type organizationWorkerRepository struct {
	execute func(context.Context, *ports.Actor, ports.JobLease) (*ports.MovieUpgradeOrganizationExecutionResult, error)
	calls   atomic.Int32
}

func (r *organizationWorkerRepository) QueueMovieUpgradeOrganization(context.Context, *ports.Actor, ports.QueueMovieUpgradeOrganization, string) (*ports.MovieUpgradeOrganizationJobReceipt, error) {
	panic("worker cannot queue authorization")
}
func (r *organizationWorkerRepository) ReadMovieUpgradeOrganizationJobReceipt(context.Context, *ports.Actor, uuid.UUID, string) (*ports.MovieUpgradeOrganizationJobReceipt, error) {
	panic("unexpected receipt read")
}
func (r *organizationWorkerRepository) CaptureMovieUpgradeOrganizationJob(context.Context, *ports.Actor, ports.JobLease) (*ports.MovieUpgradeOrganizationJobClaim, error) {
	r.calls.Add(1)
	return &ports.MovieUpgradeOrganizationJobClaim{}, nil
}
func (r *organizationWorkerRepository) ExecuteMovieUpgradeOrganization(c context.Context, a *ports.Actor, l ports.JobLease) (*ports.MovieUpgradeOrganizationExecutionResult, error) {
	return r.execute(c, a, l)
}
func (r *organizationWorkerRepository) ReadMovieUpgradeOrganizationExecutionReceipt(context.Context, *ports.Actor, uuid.UUID) (*ports.MovieUpgradeOrganizationExecutionResult, error) {
	panic("unexpected execution receipt read")
}

type organizationWorkerQueue struct {
	job     db.Job
	claimed atomic.Bool
	failed  atomic.Int32
}

func (q *organizationWorkerQueue) Claim(_ context.Context, kinds []string, owner string, _ time.Duration) (*db.Job, error) {
	if len(kinds) != 1 || kinds[0] != ports.MovieUpgradeOrganizationJobKind {
		panic("wrong kind")
	}
	if q.claimed.Swap(true) {
		return nil, nil
	}
	j := q.job
	j.LeaseOwner = pgtype.Text{String: owner, Valid: true}
	return &j, nil
}
func (q *organizationWorkerQueue) Fail(_ context.Context, _ uuid.UUID, _ string, _ int64, reason string, _ time.Duration) error {
	if reason != "upgrade_organization_manual_review" {
		panic("unsafe failure reason")
	}
	q.failed.Add(1)
	return nil
}

type organizationWorkerIngestion struct {
	calls atomic.Int32
	job   uuid.UUID
}

func (r *organizationWorkerIngestion) IngestMovieUpgradeOrganization(_ context.Context, _ *ports.Actor, job uuid.UUID) (*ports.MovieUpgradeOrganizationIngestionResult, error) {
	if job != r.job {
		panic("wrong result source")
	}
	r.calls.Add(1)
	return &ports.MovieUpgradeOrganizationIngestionResult{JobID: job, Status: "imported_pending_retirement"}, nil
}
func (r *organizationWorkerIngestion) ReadMovieUpgradeOrganizationIngestionReceipt(context.Context, *ports.Actor, uuid.UUID) (*ports.MovieUpgradeOrganizationIngestionResult, error) {
	panic("unexpected ingestion receipt")
}
func TestMovieUpgradeOrganizationWorkerPayload(t *testing.T) {
	id := uuid.New()
	good := fmt.Sprintf(`{"organization_job_id":%q}`, id.String())
	if got, e := movieUpgradeOrganizationPayload([]byte(good)); e != nil || got != id {
		t.Fatal(got, e)
	}
	for _, raw := range []string{good[:len(good)-1] + `,"organization_job_id":"` + id.String() + `"}`, strings.Replace(good, "organization_job_id", "Organization_Job_ID", 1), strings.Replace(good, id.String(), strings.ToUpper(id.String()), 1), `{"organization_job_id":null}`, `{"organization_job_id":"00000000-0000-0000-0000-000000000000"}`, good[:len(good)-1] + `,"url":"not-allowed"}`, good + `{}`, `[]`} {
		if _, e := movieUpgradeOrganizationPayload([]byte(raw)); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestMovieUpgradeOrganizationWorkerDrainsActualReturn(t *testing.T) {
	id := uuid.New()
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	q := &organizationWorkerQueue{job: db.Job{ID: id, Kind: ports.MovieUpgradeOrganizationJobKind, State: "running", Payload: []byte(fmt.Sprintf(`{"organization_job_id":%q}`, id.String())), LeaseToken: 1, Attempts: 1, MaxAttempts: 1, LeaseUntil: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}}}
	runtime := &organizationWorkerRuntime{actor: actor}
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
	repo := &organizationWorkerRepository{execute: func(_ context.Context, a *ports.Actor, l ports.JobLease) (*ports.MovieUpgradeOrganizationExecutionResult, error) {
		if a != actor || l.ID != id || l.Owner == "" {
			t.Error("lost authority")
		}
		close(entered)
		<-release
		return &ports.MovieUpgradeOrganizationExecutionResult{Status: "linked_pending_ingestion"}, nil
	}}
	w := &movieUpgradeOrganizationWorker{runtime: runtime, jobs: repo, execution: repo, ingestion: &organizationWorkerIngestion{}}
	go func() { defer close(done); a.runMovieUpgradeOrganizationWorker(ctx, q, w) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not entered")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("worker abandoned active filesystem call")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("did not exit after return")
	}
	if q.failed.Load() != 0 || repo.calls.Load() != 1 || runtime.calls.Load() != 1 {
		t.Fatal("unexpected second completion or replay")
	}
}
func TestMovieUpgradeOrganizationWorkerResumesPendingIngestion(t *testing.T) {
	id := uuid.New()
	r := &organizationWorkerRuntime{pending: []ports.MovieUpgradeOrganizationIngestionWork{{JobID: id, Actor: ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}}}}
	ingestion := &organizationWorkerIngestion{job: id}
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.ingestPendingMovieUpgrades(context.Background(), &movieUpgradeOrganizationWorker{runtime: r, ingestion: ingestion})
	if ingestion.calls.Load() != 1 {
		t.Fatal("lost completed physical result")
	}
}
