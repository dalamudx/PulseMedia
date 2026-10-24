package app

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type untrackedReviewExecutor interface {
	Run(context.Context, ports.JobLease) error
}

func (a *App) runUntrackedReviewWorker(ctx context.Context, queue *jobs.Queue, repository ports.UntrackedReviewExecutionRepository, executor untrackedReviewExecutor) {
	if queue == nil || repository == nil || executor == nil {
		return
	}
	owner := a.idgen.NewUUID().String()
	maintenance := time.NewTimer(0)
	defer maintenance.Stop()
	idle := time.NewTicker(200 * time.Millisecond)
	defer idle.Stop()
	for ctx.Err() == nil {
		select {
		case <-maintenance.C:
			if _, err := repository.RecoverUntrackedReviews(ctx, 64); err != nil && ctx.Err() == nil {
				a.logger.Error("untracked review recovery unavailable")
			}
			maintenance.Reset(3 * time.Second)
		default:
		}
		if ctx.Err() != nil {
			return
		}
		job, err := queue.Claim(ctx, []string{ports.UntrackedReviewJobKind}, owner, 2*time.Minute)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("untracked review claim unavailable")
		}
		if job != nil {
			lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
			err = runOwnedPhysical(ctx, 90*time.Second, func(call context.Context) error {
				return queue.RenewLease(call, job.ID, owner, job.LeaseToken, 2*time.Minute)
			}, func(call context.Context) error { return executor.Run(call, lease) }, 15*time.Second)
			if err != nil {
				finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if e := queue.Fail(finish, job.ID, owner, job.LeaseToken, "untracked review interrupted", 5*time.Second); e != nil {
					a.logger.Error("untracked review failure write rejected")
				}
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
		}
	}
}
