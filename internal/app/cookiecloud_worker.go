package app

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Only enabled persisted owner configurations become due. The single consumer
// retains physical ownership through cancellation and joins actual I/O before
// generic task failure handling; the repository alone schedules subsequent runs.
func (a *App) runCookieCloudWorker(ctx context.Context, queue *jobs.Queue, repo ports.CookieCloudSyncRepository, svc mediaSidecarExecutor) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := repo.RecoverExpired(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("CookieCloud recovery unavailable")
			}
		} else if err := repo.EnqueueDue(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("CookieCloud scheduling unavailable")
			}
		} else {
			job, err := queue.Claim(ctx, []string{ports.CookieCloudSyncJobKind}, owner, siteAccountStatsLease)
			if err != nil && ctx.Err() == nil {
				a.logger.Error("CookieCloud claim unavailable")
			}
			if job != nil {
				lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
				err = runSiteAccountStatsPhysical(ctx, func(ctx context.Context) error {
					return queue.RenewLease(ctx, lease.ID, lease.Owner, lease.Token, siteAccountStatsLease)
				}, func(ctx context.Context) error { return svc.Execute(ctx, lease) }, siteAccountStatsHeartbeat)
				if err != nil {
					failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					if err := queue.Fail(failCtx, lease.ID, lease.Owner, lease.Token, "CookieCloud synchronization interrupted; review_required", 0); err != nil {
						a.logger.Error("CookieCloud failure write rejected")
					}
					cancel()
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
