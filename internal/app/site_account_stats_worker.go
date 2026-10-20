package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/nexusphp"
	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	siteAccountStatsLease     = 2 * time.Minute
	siteAccountStatsHeartbeat = 15 * time.Second
	siteAccountStatsTimeout   = 60 * time.Second
)

type siteAccountStatsClientBuilder struct {
	httpClient *http.Client
	clock      ports.Clock
}

var _ ports.SiteAccountStatsClientBuilder = (*siteAccountStatsClientBuilder)(nil)

func (b *siteAccountStatsClientBuilder) NewSiteAccountStatsClient(snapshot ports.SiteAccountStatsExecution) (ports.SiteAccountStatsClient, error) {
	if snapshot.Config.Profile != ports.NexusPHPClassicAccountStatsProfile || !indexer.ValidSiteAccountID(snapshot.Config.ExpectedAccountID) {
		return nil, ports.ErrRejected
	}
	return nexusphp.NewAccountStatsClient(snapshot.SourceURL, snapshot.Cookie, b.httpClient, b.clock), nil
}

func (a *App) runSiteAccountStatsWorker(ctx context.Context, queue *jobs.Queue, repo ports.SiteAccountStatsRepository, svc mediaSidecarExecutor) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := repo.RecoverExpired(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("site account statistics recovery unavailable")
			}
		} else if err := repo.EnqueueDue(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("site account statistics scheduling unavailable")
			}
		} else {
			job, err := queue.Claim(ctx, []string{ports.SiteAccountStatsJobKind}, owner, siteAccountStatsLease)
			if err != nil && ctx.Err() == nil {
				a.logger.Error("site account statistics claim unavailable")
			}
			if job != nil {
				lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
				err = runSiteAccountStatsPhysical(ctx, func(ctx context.Context) error {
					return queue.RenewLease(ctx, lease.ID, lease.Owner, lease.Token, siteAccountStatsLease)
				}, func(ctx context.Context) error { return svc.Execute(ctx, lease) }, siteAccountStatsHeartbeat)
				if err != nil {
					// Execute has returned and released its physical source permit.
					failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					if err := queue.Fail(failCtx, lease.ID, lease.Owner, lease.Token, "site account statistics interrupted; review_required", 0); err != nil {
						a.logger.Error("site account statistics failure write rejected")
					}
					cancel()
				}
				// The collector atomically finishes the job, attempt, and next due time.
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Logical cancellation retains ownership until the actual provider call returns.
func runSiteAccountStatsPhysical(ctx context.Context, renew func(context.Context) error, execute func(context.Context) error, heartbeat time.Duration) error {
	workCtx, cancel := context.WithTimeout(ctx, siteAccountStatsTimeout)
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
