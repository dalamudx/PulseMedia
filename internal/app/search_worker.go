package app

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"sync"
	"time"
)

// Each child has an owner and a bounded context; shutdown cancels and joins all
// physical endpoint I/O. Separate finalize capacity prevents a slow indexer from
// delaying decisions past the persisted deadline.
func (a *App) runSearchWorkers(ctx context.Context, queue *jobs.Queue, sched *scheduler.AutoFulfillScheduler, search *usecaseSearch.Service, feed *usecaseSearch.FeedService, health *usecaseSearch.HealthService) {
	var group sync.WaitGroup
	for i := 0; i < 5; i++ {
		group.Add(1)
		go func(finalize bool) {
			defer group.Done()
			a.runSearchConsumer(ctx, queue, sched, search, feed, health, finalize)
		}(i == 0)
	}
	defer group.Wait()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	q := db.New(a.dbPool)
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := q.RecoverSearchJobs(ctx); err != nil {
			a.logger.Error("search recovery failed")
		}
		if _, err := health.QueueDue(ctx, 20); err != nil && ctx.Err() == nil {
			a.logger.Error("indexer health scheduling deferred")
		}
		if _, err := feed.QueueDue(ctx, 20); err != nil && ctx.Err() == nil {
			a.logger.Error("RSS scheduling deferred")
		}
		if _, err := sched.RunEvaluationBatch(ctx, &ports.Actor{Type: ports.ActorTypeSystem, ID: "scheduler"}, 20); err != nil && ctx.Err() == nil {
			a.logger.Error("subscription evaluation failed")
		}
		ready, err := q.ListReadySearchRounds(ctx, 20)
		if err == nil {
			for _, round := range ready {
				if err = q.QueueRoundFinalization(ctx, db.QueueRoundFinalizationParams{ID: a.idgen.NewUUID(), ID_2: round.ID}); err != nil {
					a.logger.Error("search finalization enqueue failed")
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
func (a *App) runSearchConsumer(ctx context.Context, queue *jobs.Queue, sched *scheduler.AutoFulfillScheduler, search *usecaseSearch.Service, feed *usecaseSearch.FeedService, health *usecaseSearch.HealthService, finalize bool) {
	owner := a.idgen.NewUUID().String()
	kinds := []string{"search_endpoint", "rss_poll", "indexer_health"}
	if finalize {
		kinds = []string{"search_round_finalize"}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := queue.Claim(ctx, kinds, owner, 45*time.Second)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("search job claim failed")
		}
		if job != nil {
			bounded, cancel := context.WithTimeout(ctx, 35*time.Second)
			heartbeatDone := make(chan struct{})
			go func() {
				defer close(heartbeatDone)
				beat := time.NewTicker(15 * time.Second)
				defer beat.Stop()
				for {
					select {
					case <-bounded.Done():
						return
					case <-beat.C:
						if e := queue.RenewLease(bounded, job.ID, owner, job.LeaseToken, 45*time.Second); e != nil {
							cancel()
							return
						}
					}
				}
			}()

			if finalize {
				var payload struct {
					RoundID uuid.UUID `json:"round_id"`
				}
				err = json.Unmarshal(job.Payload, &payload)
				if err == nil {
					err = sched.FinalizeJob(bounded, payload.RoundID, acquisition.RoundJobAuthority{ID: job.ID, Owner: owner, Token: job.LeaseToken})
				}
			} else if job.Kind == "indexer_health" {
				var payload ports.IndexerHealthJob
				err = json.Unmarshal(job.Payload, &payload)
				if err == nil {
					payload.ID = job.ID
					payload.Owner = owner
					payload.Token = job.LeaseToken
					err = health.ExecuteJob(bounded, payload)
				}
			} else if job.Kind == "rss_poll" {
				var payload ports.RSSPollJob
				err = json.Unmarshal(job.Payload, &payload)
				if err == nil {
					payload.ID = job.ID
					payload.Owner = owner
					payload.Token = job.LeaseToken
					err = feed.ExecuteJob(bounded, payload)
				}
			} else {
				var payload ports.SearchEndpointTask
				err = json.Unmarshal(job.Payload, &payload)
				if err == nil {
					payload.Lease = ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
					err = search.ExecuteEndpointJob(bounded, payload)
				}
			}
			cancel()
			<-heartbeatDone
			if err != nil && ctx.Err() == nil {
				var payload ports.RSSPollJob
				_ = json.Unmarshal(job.Payload, &payload)
				if e := queue.Fail(ctx, job.ID, owner, job.LeaseToken, "search execution deferred", feed.RetryDelay(ctx, payload.IndexerID)); e != nil {
					a.logger.Error("search job write rejected")
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
