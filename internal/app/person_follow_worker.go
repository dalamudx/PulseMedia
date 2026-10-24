package app

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/jackc/pgx/v5/pgxpool"
)

type personFollowSubscriptionCreator struct {
	pool      *pgxpool.Pool
	ids       ports.IDGen
	provider  ports.MetadataProvider
	episodes  ports.TVEpisodeProvider
	protector ports.SecretProtector
}

var _ ports.PersonFollowSubscriptionCreator = (*personFollowSubscriptionCreator)(nil)

func (c *personFollowSubscriptionCreator) CreatePersonFollowSubscription(ctx context.Context, fence ports.PersonFollowCreationFence, actor *ports.Actor, input ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
	service := subscription.NewSubscriptionCreationService(postgres.NewPersonFollowCreationStore(c.pool, c.ids, fence), c.provider, c.episodes, c.protector)
	return service.Create(ctx, actor, input, key)
}

// Page completion owns its next dispatch; a cancelled worker waits for the
// actual provider call through the existing physical-call lifecycle helper.
func (a *App) runPersonFollowWorker(ctx context.Context, queue *jobs.Queue, repo ports.PersonFollowSyncRepository, service mediaSidecarExecutor) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := repo.RecoverExpired(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("person follow recovery unavailable")
			}
		} else if err = repo.EnqueueDue(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("person follow scheduling unavailable")
			}
		} else {
			job, err := queue.Claim(ctx, []string{ports.PersonFollowSyncJobKind}, owner, doubanWishLease)
			if err != nil && ctx.Err() == nil {
				a.logger.Error("person follow claim unavailable")
			}
			if job != nil {
				lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
				err = runDoubanWishPhysical(ctx, func(ctx context.Context) error {
					return queue.RenewLease(ctx, lease.ID, lease.Owner, lease.Token, doubanWishLease)
				}, func(ctx context.Context) error { return service.Execute(ctx, lease) }, doubanWishHeartbeat)
				if err != nil {
					retry := time.Duration(0)
					if rate, ok := ports.IsRateLimited(err); ok {
						retry = max(time.Duration(0), min(rate.RetryAfter, 24*time.Hour))
					}
					failed, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					if failErr := queue.Fail(failed, lease.ID, lease.Owner, lease.Token, "person follow execution interrupted", retry); failErr != nil {
						a.logger.Error("person follow failure write rejected")
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
