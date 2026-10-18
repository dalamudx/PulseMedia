package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

const completeTVSubmissionKind = "complete_tv_coverage_download_submission"
const completeTVLease = 90 * time.Second

// runCompleteTVWorker is owned and drained by App.Run. It never delegates
// physical I/O to an unjoined goroutine or to the generic single-unit worker.
func (a *App) runCompleteTVWorker(ctx context.Context, queue *jobs.Queue, executor ports.CompleteTVExecutor, observer ports.CompleteTVObservationRepository, recovery ports.CompleteTVRecoveryRepository, tracking ports.CompleteTVTrackingRepository, inspector ports.TorrentMetainfoInspector, scheduler ports.CompleteTVTrackingScheduler) {
	owner := a.idgen.NewUUID().String()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := recovery.RecoverCompleteTVJobs(ctx); err != nil {
			if ctx.Err() == nil {
				a.logger.Error("complete TV recovery unavailable")
			}
		} else {
			if err := scheduler.ScheduleCompleteTVTracking(ctx); err != nil {
				if ctx.Err() == nil {
					a.logger.Error("complete TV tracking scheduling unavailable")
				}
			}
			job, err := queue.Claim(ctx, []string{completeTVSubmissionKind, ports.CompleteTVObservationJobKind, ports.CompleteTVTrackingJobKind}, owner, completeTVLease)
			if err != nil {
				if ctx.Err() == nil {
					a.logger.Error("complete TV claim unavailable")
				}
			} else if job != nil {
				a.executeCompleteTVJob(ctx, queue, executor, observer, tracking, inspector, job)
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) executeCompleteTVJob(ctx context.Context, queue *jobs.Queue, executor ports.CompleteTVExecutor, observer ports.CompleteTVObservationRepository, tracking ports.CompleteTVTrackingRepository, inspector ports.TorrentMetainfoInspector, job *db.Job) {
	work, cancel := context.WithTimeout(ctx, 2*time.Minute)
	done := make(chan struct{})
	renewed := make(chan struct{})
	// A failed heartbeat cancels logical work. The synchronous caller still owns
	// physical execution until the adapter actually returns and the goroutine joins.
	go func() {
		defer close(renewed)
		tick := time.NewTicker(completeTVLease / 3)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-work.Done():
				return
			case <-tick.C:
				if queue.RenewLease(work, job.ID, job.LeaseOwner.String, job.LeaseToken, completeTVLease) != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := handleCompleteTVJob(work, executor, observer, tracking, inspector, job)
	close(done)
	cancel()
	<-renewed
	if ctx.Err() != nil {
		return
	}
	// Successful native UoWs finish jobs atomically. Never finish business results
	// here; a failed/stale callback remains recoverable under a fresh observation.
	if err != nil {
		_ = queue.Fail(ctx, job.ID, job.LeaseOwner.String, job.LeaseToken, "complete TV work needs observation or manual review", 30*time.Second)
	}
}

func handleCompleteTVJob(ctx context.Context, executor ports.CompleteTVExecutor, observer ports.CompleteTVObservationRepository, tracking ports.CompleteTVTrackingRepository, inspector ports.TorrentMetainfoInspector, job *db.Job) error {
	var payload struct {
		Operation uuid.UUID `json:"operation_id"`
		Authority uuid.UUID `json:"complete_tv_authority"`
		Task      uuid.UUID `json:"task_id"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || payload.Operation == uuid.Nil || !job.LeaseOwner.Valid {
		return errors.New("invalid complete TV job")
	}
	lease := ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}
	switch job.Kind {
	case completeTVSubmissionKind:
		if payload.Authority != payload.Operation {
			return errors.New("invalid complete TV authority")
		}
		_, err := executor.SendCompleteTV(ctx, lease, payload.Operation)
		return err
	case ports.CompleteTVTrackingJobKind:
		if payload.Task == uuid.Nil {
			return errors.New("invalid complete TV tracking task")
		}
		_, err := executor.ObserveCompleteTVTracking(ctx, tracking, inspector, ports.CompleteTVTrackingRequest{Lease: lease, TaskID: payload.Task, OperationID: payload.Operation})
		return err
	case ports.CompleteTVObservationJobKind:
		if payload.Task == uuid.Nil {
			return errors.New("invalid complete TV task")
		}
		_, err := executor.ObserveCompleteTV(ctx, observer, ports.CompleteTVObservationRequest{Lease: lease, TaskID: payload.Task, OperationID: payload.Operation})
		return err
	default:
		return errors.New("invalid complete TV kind")
	}
}
