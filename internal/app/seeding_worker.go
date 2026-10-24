package app

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type seedingClientFactory struct {
	sources ports.SeedingClientSourceReader
}

var _ ports.SeedingExecutionFactory = (*seedingClientFactory)(nil)

func (f *seedingClientFactory) OpenSeedingExecution(ctx context.Context, id uuid.UUID, stamp string) (ports.SeedingExecutionNative, error) {
	var out ports.SeedingExecutionNative
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if f == nil || f.sources == nil {
		return out, ports.ErrUnavailable
	}
	source, err := f.sources.ReadSeedingClientSource(ctx, id, stamp)
	if err != nil {
		return out, err
	}
	if source.ClientID != id || source.ConfigStamp != stamp {
		return out, ports.ErrSeedingConfigConflict
	}
	native, err := NativeDownloadClient(source.Kind, source.URL, source.Username, source.Credential)
	if err != nil {
		return out, ports.ErrRejected
	}
	capable, ok := native.(interface {
		SeedingObserver() ports.SeedingReadClient
		SeedingRemover(ports.SeedingRemovalTarget, ports.SeedingRemovalAuthorizer) (ports.SeedingRemovalClient, error)
	})
	if !ok {
		return out, ports.ErrUnavailable
	}
	return ports.SeedingExecutionNative{Reader: capable.SeedingObserver(), Remover: capable.SeedingRemover}, nil
}

type seedingScheduler interface {
	RecoverExpired(context.Context) error
	EnqueueDue(context.Context) error
}

// A single owned consumer runs only persisted enabled configurations. Generic
// Fail must not replace the repository's atomic outcome or recovery decisions.
func (a *App) runSeedingWorker(ctx context.Context, queue *jobs.Queue, repo seedingScheduler, svc mediaSidecarExecutor) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := repo.RecoverExpired(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("seeding recovery unavailable")
			}
		} else if err := repo.EnqueueDue(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("seeding scheduling unavailable")
			}
		} else {
			job, err := queue.Claim(ctx, []string{"seeding_removal"}, owner, siteAccountStatsLease)
			if err != nil && ctx.Err() == nil {
				a.logger.Error("seeding claim unavailable")
			}
			if job != nil {
				lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
				err = runSiteAccountStatsPhysical(ctx, func(c context.Context) error {
					return queue.RenewLease(c, lease.ID, lease.Owner, lease.Token, siteAccountStatsLease)
				}, func(c context.Context) error { return svc.Execute(c, lease) }, siteAccountStatsHeartbeat)
				if err != nil && ctx.Err() == nil {
					a.logger.Error("seeding execution interrupted; original recovery retained")
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
