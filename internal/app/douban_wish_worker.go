package app

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	doubanWishLease       = 2 * time.Minute
	doubanWishHeartbeat   = 15 * time.Second
	doubanWishPageTimeout = 15 * time.Minute
)

type doubanWishSubscriptionCreator struct {
	pool      *pgxpool.Pool
	ids       ports.IDGen
	provider  ports.MetadataProvider
	episodes  ports.TVEpisodeProvider
	protector ports.SecretProtector
}

var _ ports.DoubanWishSubscriptionCreator = (*doubanWishSubscriptionCreator)(nil)

func (c *doubanWishSubscriptionCreator) CreateDoubanWishSubscription(ctx context.Context, fence ports.DoubanWishCreationFence, actor *ports.Actor, input ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
	svc := subscription.NewSubscriptionCreationService(postgres.NewDoubanWishCreationStore(c.pool, c.ids, fence), c.provider, c.episodes, c.protector)
	return svc.Create(ctx, actor, input, key)
}

func (a *App) runDoubanWishWorker(ctx context.Context, queue *jobs.Queue, repo ports.DoubanWishSyncRepository, svc mediaSidecarExecutor) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var nextScheduleReport time.Time
	for ctx.Err() == nil {
		if err := repo.RecoverExpired(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("douban wish recovery unavailable")
			}
		} else {
			a.enqueueScheduled(ctx, ports.StageDoubanWishSchedule, &nextScheduleReport, repo.EnqueueDue)
			if ctx.Err() != nil {
				return
			}
			job, err := queue.Claim(ctx, []string{ports.DoubanWishSyncJobKind}, owner, doubanWishLease)
			if err != nil && ctx.Err() == nil {
				a.logger.Error("douban wish claim unavailable")
			}
			if job != nil {
				lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
				err = runDoubanWishPhysical(ctx, func(ctx context.Context) error {
					return queue.RenewLease(ctx, lease.ID, lease.Owner, lease.Token, doubanWishLease)
				}, func(ctx context.Context) error { return svc.Execute(ctx, lease) }, doubanWishHeartbeat)
				if err != nil {
					// Execute has returned and released its physical permit before failure writeback.
					failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					if err := queue.Fail(failCtx, lease.ID, lease.Owner, lease.Token, "douban wish interrupted; review_required", 0); err != nil {
						a.logger.Error("douban wish failure write rejected")
					}
					cancel()
				}
				// FinishPage owns completion and continuation in the same transaction.
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Logical cancellation never releases ownership of an executing provider call.
func runDoubanWishPhysical(ctx context.Context, renew func(context.Context) error, execute func(context.Context) error, heartbeat time.Duration) error {
	workCtx, cancel := context.WithTimeout(ctx, doubanWishPageTimeout)
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
