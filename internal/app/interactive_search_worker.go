package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func (a *App) runAllSearchWorkers(ctx context.Context, queue *jobs.Queue, sched *scheduler.AutoFulfillScheduler, search *usecaseSearch.Service, feed *usecaseSearch.FeedService, health *usecaseSearch.HealthService, repo ports.InteractiveSearchRepository, automaticTVRounds ports.AutomaticTVRoundRepository, automaticTVFinalizer ports.AutomaticTVAcquisitionRepository) {
	var group sync.WaitGroup
	group.Go(func() { a.runSearchWorkers(ctx, queue, sched, search, feed, health) })
	group.Go(func() { a.runInteractiveSearchWorkers(ctx, queue, repo, search) })
	group.Go(func() { a.runAutomaticTVSearchWorkers(ctx, queue, automaticTVRounds, automaticTVFinalizer, search) })
	group.Wait()
}

func (a *App) runInteractiveSearchWorkers(ctx context.Context, queue *jobs.Queue, repo ports.InteractiveSearchRepository, executor ports.InteractiveSearchExecutor) {
	if repo == nil || executor == nil {
		return
	}
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() { a.runInteractiveSearchConsumer(ctx, queue, repo, executor) })
	}
	defer group.Wait()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := repo.ExpireInteractiveSearches(ctx, 64); err != nil && ctx.Err() == nil {
			a.logger.Error("interactive search recovery deferred")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runInteractiveSearchConsumer(ctx context.Context, queue *jobs.Queue, repo ports.InteractiveSearchRepository, executor ports.InteractiveSearchExecutor) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		job, err := queue.Claim(ctx, []string{"interactive_search_source"}, owner, 12*time.Second)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("interactive search claim deferred")
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		var task ports.InteractiveSearchSourceTask
		err = json.Unmarshal(job.Payload, &task)
		task.Lease = ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
		var lease *ports.InteractiveSearchSourceLease
		if err == nil {
			lease, err = repo.LoadInteractiveSearchSource(ctx, task)
		}
		if err != nil || lease == nil {
			if ctx.Err() == nil {
				_ = queue.Fail(ctx, job.ID, owner, job.LeaseToken, "interactive search authority unavailable", time.Second)
			}
			continue
		}
		work, cancel := context.WithDeadline(ctx, lease.Deadline)
		heartbeatDone := make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			beat := time.NewTicker(3 * time.Second)
			defer beat.Stop()
			for {
				select {
				case <-work.Done():
					return
				case <-beat.C:
					if e := queue.RenewLease(work, job.ID, owner, job.LeaseToken, 12*time.Second); e != nil {
						cancel()
						return
					}
				}
			}
		}()
		executionErr := executor.ExecuteInteractiveSearchSource(work, *lease)
		// Neither cancellation nor lease loss detaches real I/O from this owner.
		// The synchronous call above must return before the physical worker is reused.
		cancel()
		<-heartbeatDone
		outcome := "succeeded"
		if errors.Is(executionErr, ports.ErrInteractiveSearchDeferred) {
			outcome = "deferred"
		} else if errors.Is(executionErr, context.DeadlineExceeded) || errors.Is(executionErr, ports.ErrTimeout) {
			outcome = "timeout"
		} else if executionErr != nil {
			outcome = "failed"
		}
		if ctx.Err() != nil {
			return
		}
		if e := repo.FinishInteractiveSearchSource(ctx, *lease, outcome); e != nil && ctx.Err() == nil {
			a.logger.Debug("interactive search completion fenced")
		}
	}
}
