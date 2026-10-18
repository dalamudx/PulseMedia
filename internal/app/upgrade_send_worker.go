package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"time"
)

func movieUpgradeSendPayload(raw []byte) (command, op, task, actor uuid.UUID, err error) {
	fail := ports.ErrUpgradeReservationConflict
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return command, op, task, actor, fail
	}
	fields := make(map[string]uuid.UUID, 4)
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return command, op, task, actor, fail
		}
		k, ok := t.(string)
		if !ok || (k != "command_id" && k != "operation_id" && k != "task_id" && k != "actor_id") {
			return command, op, task, actor, fail
		}
		if _, ok = fields[k]; ok {
			return command, op, task, actor, fail
		}
		var v string
		if d.Decode(&v) != nil {
			return command, op, task, actor, fail
		}
		id, e := uuid.Parse(v)
		if e != nil || id == uuid.Nil || id.String() != v {
			return command, op, task, actor, fail
		}
		fields[k] = id
	}
	if t, e = d.Token(); e != nil || t != json.Delim('}') || len(fields) != 4 {
		return command, op, task, actor, fail
	}
	if _, e = d.Token(); !errors.Is(e, io.EOF) {
		return command, op, task, actor, fail
	}
	return fields["command_id"], fields["operation_id"], fields["task_id"], fields["actor_id"], nil
}
func (a *App) runMovieUpgradeSendJob(ctx context.Context, q movieUpgradeObservationQueue, e ports.MovieUpgradeSendJobExecutor, j *db.Job) {
	command, op, task, actor, err := movieUpgradeSendPayload(j.Payload)
	if err == nil && j.Kind == ports.MovieUpgradeSendJobKind && j.State == "running" && j.Attempts == 1 && j.MaxAttempts == 1 && j.LeaseOwner.Valid && j.LeaseToken > 0 && j.LeaseUntil.Valid {
		req := ports.MovieUpgradeSendJobRequest{CommandID: command, OperationID: op, TaskID: task, Lease: ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}}
		// No heartbeat, adoption or separate Succeed. Own I/O until its actual return.
		err = e.ExecuteMovieUpgradeSendJob(ctx, req, actor)
		if err == nil {
			return
		}
	}
	reason := "movie upgrade send requires manual review"
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = q.Fail(cleanup, j.ID, j.LeaseOwner.String, j.LeaseToken, reason, time.Second); err != nil {
		a.logger.Warn("movie upgrade send job remains fenced", slog.String("job_id", j.ID.String()))
	}
}
func (a *App) runMovieUpgradeSendWorker(ctx context.Context, q movieUpgradeObservationQueue, e ports.MovieUpgradeSendJobExecutor) {
	if q == nil || e == nil {
		return
	}
	owner := a.idgen.NewUUIDString()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		claim, cancel := context.WithTimeout(ctx, 5*time.Second)
		j, err := q.Claim(claim, []string{ports.MovieUpgradeSendJobKind}, owner, 2*time.Minute)
		cancel()
		if err != nil && ctx.Err() == nil {
			a.logger.Warn("movie upgrade send claim unavailable")
		}
		if err == nil && j != nil {
			a.runMovieUpgradeSendJob(ctx, q, e, j)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}
