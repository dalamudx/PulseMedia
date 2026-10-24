package app

import (
	"context"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const globalRateConsumers = 3

type downloadGlobalRatesBuilder struct{}

var _ ports.DownloadGlobalRatesBuilder = downloadGlobalRatesBuilder{}

func (downloadGlobalRatesBuilder) NewDownloadGlobalRatesReader(source ports.DownloadGlobalRatesSource) (ports.DownloadGlobalRatesReader, error) {
	client, err := NativeDownloadClient(source.ClientType, source.URL, source.Username, source.Password)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	reader, ok := client.(ports.DownloadGlobalRatesReader)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return reader, nil
}

// App owns and cancels the shared context, then joins this coordinator. Each
// consumer has a distinct queue owner; the coordinator joins all physical work.
func (a *App) runDownloadGlobalRatesWorker(ctx context.Context, queue *jobs.Queue, repo ports.DownloadGlobalRatesRepository, svc mediaSidecarExecutor) {
	var group sync.WaitGroup
	for range globalRateConsumers {
		owner := a.idgen.NewUUID().String()
		group.Add(1)
		go func() { defer group.Done(); a.runDownloadGlobalRatesConsumer(ctx, queue, repo, svc, owner) }()
	}
	group.Wait()
}

func (a *App) runDownloadGlobalRatesConsumer(ctx context.Context, queue *jobs.Queue, repo ports.DownloadGlobalRatesRepository, svc mediaSidecarExecutor, owner string) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := repo.RecoverExpired(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("global transfer observation recovery unavailable")
			}
		} else if err := repo.EnqueueDue(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("global transfer observation scheduling unavailable")
			}
		} else {
			job, err := queue.Claim(ctx, []string{ports.DownloadGlobalRatesJobKind}, owner, 2*time.Minute)
			if err != nil && ctx.Err() == nil {
				a.logger.Error("global transfer observation claim unavailable")
			}
			if job != nil {
				lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
				err = runSiteAccountStatsPhysical(ctx, func(ctx context.Context) error {
					return queue.RenewLease(ctx, lease.ID, lease.Owner, lease.Token, 2*time.Minute)
				}, func(ctx context.Context) error { return svc.Execute(ctx, lease) }, 15*time.Second)
				if err != nil {
					// Execute has returned and released its own source permit before Fail.
					failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					if err = queue.Fail(failCtx, lease.ID, lease.Owner, lease.Token, "global transfer observation interrupted", 0); err != nil {
						a.logger.Error("global transfer observation failure write rejected")
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
