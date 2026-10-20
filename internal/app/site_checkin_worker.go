package app

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/nexusphp"
	domainindexer "github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const (
	siteCheckinLease     = 2 * time.Minute
	siteCheckinHeartbeat = 15 * time.Second
)

type siteCheckinClientBuilder struct {
	httpClient *http.Client
	clock      ports.Clock
}

var _ ports.SiteCheckinClientBuilder = (*siteCheckinClientBuilder)(nil)

func (b *siteCheckinClientBuilder) NewSiteCheckinClient(snapshot ports.SiteCheckinExecution) (ports.SiteCheckinNativeClient, error) {
	c := snapshot.Config
	day, err := time.Parse("2006-01-02", snapshot.CalendarDay)
	if err != nil || day.Format("2006-01-02") != snapshot.CalendarDay || snapshot.DayID == uuid.Nil || snapshot.ExecutionID == uuid.Nil || c.IndexerID == uuid.Nil || !c.Enabled || c.Version < 1 || c.SourceConfigVersion < 1 || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) || c.Profile != ports.NexusPHP1103CheckinProfile || !domainindexer.ValidSiteAccountID(c.ExpectedAccountID) || !domainindexer.ValidSiteCheckinSchedule(c.Timezone, c.DailyAt) || strings.TrimSpace(snapshot.SourceURL) == "" || strings.TrimSpace(snapshot.Cookie) == "" || strings.ContainsAny(snapshot.Cookie, "\r\n") {
		return nil, ports.ErrRejected
	}
	if b == nil || b.clock == nil {
		return nil, ports.ErrUnavailable
	}
	return nexusphp.NewCheckinClient(snapshot.SourceURL, snapshot.Cookie, b.httpClient, b.clock), nil
}

func (a *App) runSiteCheckinWorker(ctx context.Context, queue *jobs.Queue, repo ports.SiteCheckinRepository, svc mediaSidecarExecutor) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := repo.RecoverExpired(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("site checkin recovery unavailable")
			}
		} else if err := repo.EnqueueDue(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("site checkin scheduling unavailable")
			}
		} else {
			job, err := queue.Claim(ctx, []string{ports.SiteCheckinJobKind, ports.SiteCheckinObservationJobKind}, owner, siteCheckinLease)
			if err != nil && ctx.Err() == nil {
				a.logger.Error("site checkin claim unavailable")
			}
			if job != nil {
				lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
				err = runSiteAccountStatsPhysical(ctx, func(ctx context.Context) error {
					return queue.RenewLease(ctx, lease.ID, lease.Owner, lease.Token, siteCheckinLease)
				}, func(ctx context.Context) error { return svc.Execute(ctx, lease) }, siteCheckinHeartbeat)
				if err != nil {
					// Execute has returned and released its physical source permit.
					failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					if err := queue.Fail(failCtx, lease.ID, lease.Owner, lease.Token, "site checkin interrupted; review_required", 0); err != nil {
						a.logger.Error("site checkin failure write rejected")
					}
					cancel()
				}
				// Finish owns job/day/execution completion; only the store schedules recovery.
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
