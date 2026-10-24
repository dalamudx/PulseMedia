package app

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

type mediaSidecarExecutor interface {
	Execute(context.Context, ports.JobLease) error
}

// runMediaSidecarWorker is owned and awaited by App.Run. Failed attempts are not
// automatically replayed: max_attempts=1 and expiry requires manual observation.
func (a *App) runMediaSidecarWorker(ctx context.Context, queue *jobs.Queue, svc mediaSidecarExecutor) {
	a.runMediaAttachmentWorker(ctx, queue, svc, ports.MediaSidecarJobKind)
}

func (a *App) runMediaSubtitleWorker(ctx context.Context, queue *jobs.Queue, svc mediaSidecarExecutor) {
	a.runMediaAttachmentWorker(ctx, queue, svc, ports.MediaSubtitleJobKind)
}

func (a *App) runMediaAttachmentWorker(ctx context.Context, queue *jobs.Queue, svc mediaSidecarExecutor, kind string) {
	owner := a.idgen.NewUUID().String()
	q := db.New(a.dbPool)
	recoverJobs := q.RecoverExpiredMediaSidecarJobs
	if kind == ports.MediaSubtitleJobKind {
		recoverJobs = q.RecoverExpiredMediaSubtitleJobs
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := recoverJobs(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("metadata job recovery unavailable")
		}
		job, err := queue.Claim(ctx, []string{kind}, owner, 2*time.Minute)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("metadata job claim unavailable")
		}
		if job != nil {
			lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
			err = runMediaSidecarPhysical(ctx, func(ctx context.Context) error {
				return queue.RenewLease(ctx, job.ID, owner, job.LeaseToken, 2*time.Minute)
			}, func(ctx context.Context) error { return svc.Execute(ctx, lease) }, 15*time.Second)
			if err != nil {
				// The synchronous usecase and physical cleanup have actually returned.
				// A valid original lease can mark review even during shutdown. An expired
				// or foreign callback is rejected and left for kind-specific recovery.
				finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if failErr := queue.Fail(finishCtx, job.ID, owner, job.LeaseToken, "metadata interrupted; review_required", 0); failErr != nil {
					a.logger.Error("metadata failure write rejected")
				}
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Cancellation fences writeback; it cannot detach a still-running physical call.
// This child is locally owned, canceled and synchronously awaited on every exit.
func runMediaSidecarPhysical(ctx context.Context, renew func(context.Context) error, execute func(context.Context) error, heartbeat time.Duration) error {
	return runOwnedPhysical(ctx, 90*time.Second, renew, execute, heartbeat)
}

func runOwnedPhysical(ctx context.Context, timeout time.Duration, renew func(context.Context) error, execute func(context.Context) error, heartbeat time.Duration) error {
	workCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- execute(workCtx) }()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return errors.Join(err, workCtx.Err())
		case <-workCtx.Done():
			return errors.Join(workCtx.Err(), <-done)
		case <-ticker.C:
			if err := renew(workCtx); err != nil {
				cancel()
				return errors.Join(err, <-done)
			}
		}
	}
}
