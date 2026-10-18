package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type movieUpgradeObservationQueue interface {
	Claim(context.Context, []string, string, time.Duration) (*db.Job, error)
	Fail(context.Context, uuid.UUID, string, int64, string, time.Duration) error
}

func movieUpgradeObservationPayload(raw []byte) (op, task, actor uuid.UUID, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, e := dec.Token()
	if e != nil || token != json.Delim('{') {
		return op, task, actor, ports.ErrMovieUpgradeObservationReview
	}
	fields := make(map[string]uuid.UUID, 3)
	for dec.More() {
		token, e = dec.Token()
		if e != nil {
			return op, task, actor, ports.ErrMovieUpgradeObservationReview
		}
		key, ok := token.(string)
		if !ok || (key != "operation_id" && key != "task_id" && key != "actor_id") {
			return op, task, actor, ports.ErrMovieUpgradeObservationReview
		}
		if _, exists := fields[key]; exists {
			return op, task, actor, ports.ErrMovieUpgradeObservationReview
		}
		var value string
		if dec.Decode(&value) != nil {
			return op, task, actor, ports.ErrMovieUpgradeObservationReview
		}
		id, e := uuid.Parse(value)
		if e != nil || id == uuid.Nil || id.String() != value {
			return op, task, actor, ports.ErrMovieUpgradeObservationReview
		}
		fields[key] = id
	}
	if token, e = dec.Token(); e != nil || token != json.Delim('}') || len(fields) != 3 {
		return op, task, actor, ports.ErrMovieUpgradeObservationReview
	}
	if _, e = dec.Token(); !errors.Is(e, io.EOF) {
		return op, task, actor, ports.ErrMovieUpgradeObservationReview
	}
	return fields["operation_id"], fields["task_id"], fields["actor_id"], nil
}

func (a *App) runMovieUpgradeObservationJob(ctx context.Context, q movieUpgradeObservationQueue, runtime ports.MovieUpgradeObservationRuntime, repo ports.MovieUpgradeNativeJobObservationRepository, job *db.Job) {
	fail := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := q.Fail(cleanup, job.ID, job.LeaseOwner.String, job.LeaseToken, "movie upgrade observation requires manual review", time.Second); err != nil {
			a.logger.Warn("movie upgrade observation job remains fenced", slog.String("job_id", job.ID.String()))
		}
	}
	op, task, actor, err := movieUpgradeObservationPayload(job.Payload)
	if err != nil || job.Kind != ports.MovieUpgradeObservationJobKind || job.State != "running" || job.Attempts != 1 || job.MaxAttempts != 1 || !job.LeaseOwner.Valid || job.LeaseToken <= 0 || !job.LeaseUntil.Valid {
		fail()
		return
	}
	req := ports.MovieUpgradeObservationJobRequest{OperationID: op, TaskID: task, Lease: ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}, AdmissionKey: "upgrade-observe-admit:" + job.ID.String(), ResultKey: "upgrade-observe-result:" + job.ID.String()}
	original, err := runtime.MovieUpgradeObservationActor(ctx, req, actor)
	if err != nil {
		fail()
		return
	}
	// This synchronous call owns physical I/O. There is no heartbeat or Succeed;
	// the repository commits the observation result and job success together.
	receipt, err := repo.ObserveMovieUpgradeJob(ctx, original, req)
	if err != nil || receipt == nil || receipt.Disposition != "manual_review" || receipt.ExecutionAllowed {
		fail()
	}
}

func (a *App) runMovieUpgradeObservationWorker(ctx context.Context, q movieUpgradeObservationQueue, runtime ports.MovieUpgradeObservationRuntime, repo ports.MovieUpgradeNativeJobObservationRepository) {
	if q == nil || runtime == nil || repo == nil {
		return
	}
	owner := a.idgen.NewUUIDString()
	schedule := func() {
		work, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if _, err := runtime.ScheduleMovieUpgradeObservations(work); err != nil && ctx.Err() == nil {
			a.logger.Warn("movie upgrade observation scheduling unavailable")
		}
	}
	schedule()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	scheduling := time.NewTicker(30 * time.Second)
	defer scheduling.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		job, err := q.Claim(work, []string{ports.MovieUpgradeObservationJobKind}, owner, 2*time.Minute)
		cancel()
		if err != nil && ctx.Err() == nil {
			a.logger.Warn("movie upgrade observation claim unavailable")
		}
		if err == nil && job != nil {
			a.runMovieUpgradeObservationJob(ctx, q, runtime, repo, job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-scheduling.C:
			schedule()
		}
	}
}
