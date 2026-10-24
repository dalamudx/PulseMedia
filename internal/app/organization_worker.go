package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"time"
)

// Owned by App.Run: cancellation does not release an in-flight physical root slot.
func (a *App) runOrganizationWorkerWithWake(ctx context.Context, queue *jobs.Queue, svc *transfer.Service, wake <-chan struct{}, reuse ...*transfer.LibraryReuseService) {
	owner := a.idgen.NewUUID().String()
	q := db.New(a.dbPool)
	kinds := []string{"download_organization"}
	var local *transfer.LibraryReuseService
	if len(reuse) == 1 && reuse[0] != nil {
		local = reuse[0]
		kinds = append(kinds, "library_reuse")
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := q.RecoverExpiredOrganizationJobs(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("organization job recovery failed")
		}
		if local != nil {
			if _, err := q.RecoverLibraryReuseJobs(ctx); err != nil && ctx.Err() == nil {
				a.logger.Error("local reuse recovery failed")
			}
		}
		job, err := queue.Claim(ctx, kinds, owner, 5*time.Minute)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("organization job claim failed")
		}
		if job != nil {
			workCtx, cancel := context.WithTimeout(ctx, 110*time.Second)
			lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
			var err error
			if job.Kind == "library_reuse" {
				err = local.Execute(workCtx, lease)
			} else {
				err = svc.OrganizeDownload(workCtx, lease)
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
			// The usecase atomically settles successful/review work. Only a retryable
			// error is rescheduled here; never persist a raw path/adapter error.
			if err != nil {
				if failErr := queue.Fail(ctx, job.ID, owner, job.LeaseToken, "organization interrupted; observation required", 30*time.Second); failErr != nil {
					a.logger.Error("organization failure write rejected")
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
		}
	}
}
