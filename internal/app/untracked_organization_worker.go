package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"time"
)

func (a *App) runUntrackedOrganizationWorker(ctx context.Context, queue *jobs.Queue, executor ports.UntrackedOrganizationExecutionRepository) {
	if queue == nil || executor == nil {
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
			if _, err := executor.RecoverUntrackedOrganizations(ctx, 64); err != nil && ctx.Err() == nil {
				a.logger.Error("untracked organization recovery unavailable")
			}
			maintenance.Reset(3 * time.Second)
		default:
		}
		if ctx.Err() != nil {
			return
		}
		job, err := queue.Claim(ctx, []string{ports.UntrackedOrganizationJobKind}, owner, 2*time.Minute)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("untracked organization claim unavailable")
		}
		if job != nil {
			lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
			err = runOwnedPhysical(ctx, 30*time.Minute, func(call context.Context) error {
				return queue.RenewLease(call, job.ID, owner, job.LeaseToken, 2*time.Minute)
			}, func(call context.Context) error { return executor.OrganizeUntrackedFile(call, lease) }, 15*time.Second)
			if err != nil {
				finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if e := queue.Fail(finish, job.ID, owner, job.LeaseToken, "untracked organization interrupted", 5*time.Second); e != nil {
					a.logger.Error("untracked organization failure write rejected")
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
