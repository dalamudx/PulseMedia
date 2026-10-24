package app

import (
	"context"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type automaticTVObservationExecutor interface {
	ObserveAutomaticTV(context.Context, ports.AutomaticTVObservationRepository, ports.AutomaticTVObservationRequest) (*ports.AutomaticTVObservationReceipt, error)
}

func (a *App) runAutomaticTVDownloadWorkers(ctx context.Context, queue *jobs.Queue, recovery ports.AutomaticTVSubmissionRecoveryRepository, sender ports.AutomaticTVSendJobExecutor, repository ports.AutomaticTVObservationRepository, observer automaticTVObservationExecutor, organizationWake chan<- struct{}) {
	if queue == nil || recovery == nil || sender == nil || repository == nil || observer == nil {
		return
	}
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); a.runAutomaticTVSubmissionWorkers(ctx, queue, recovery, sender) }()
	go func() {
		defer group.Done()
		a.runAutomaticTVObservationWorkers(ctx, queue, repository, observer, organizationWake)
	}()
	group.Wait()
}

func (a *App) runAutomaticTVObservationWorkers(ctx context.Context, queue *jobs.Queue, repository ports.AutomaticTVObservationRepository, observer automaticTVObservationExecutor, organizationWake chan<- struct{}) {
	if queue == nil || repository == nil || observer == nil {
		return
	}
	var group sync.WaitGroup
	consumers, cancelConsumers := context.WithCancel(ctx)
	defer func() { cancelConsumers(); group.Wait() }()
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			a.runAutomaticTVObservationConsumer(consumers, queue, repository, observer, organizationWake)
		}()
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		maintenance, cancel := context.WithTimeout(ctx, 5*time.Second)
		if _, err := repository.RecoverAutomaticTVObservations(maintenance, 64); err != nil && ctx.Err() == nil {
			a.logger.Error("automatic TV observation recovery deferred")
		}
		if _, err := repository.ScheduleAutomaticTVObservations(maintenance, 64); err != nil && ctx.Err() == nil {
			a.logger.Error("automatic TV observation scheduling deferred")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runAutomaticTVObservationConsumer(ctx context.Context, queue *jobs.Queue, repository ports.AutomaticTVObservationRepository, observer automaticTVObservationExecutor, organizationWake chan<- struct{}) {
	owner := a.idgen.NewUUID().String()
	q := db.New(a.dbPool)
	idle := time.NewTicker(200 * time.Millisecond)
	defer idle.Stop()
	for ctx.Err() == nil {
		job, err := queue.Claim(ctx, []string{ports.AutomaticTVObservationJobKind}, owner, 30*time.Second)
		if err != nil || job == nil {
			if err != nil && ctx.Err() == nil {
				a.logger.Debug("automatic TV observation claim deferred")
			}
			select {
			case <-ctx.Done():
				return
			case <-idle.C:
			}
			continue
		}
		lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
		var payload struct {
			OperationID uuid.UUID `json:"operation_id"`
			TaskID      uuid.UUID `json:"task_id"`
			Authority   uuid.UUID `json:"automatic_tv_authority"`
		}
		valid := decodeAutomaticTVPayload(job.Payload, &payload) && payload.OperationID != uuid.Nil && payload.TaskID != uuid.Nil && payload.Authority == payload.OperationID && job.DedupeKey.Valid && job.DedupeKey.String == payload.OperationID.String()
		if valid {
			request := ports.AutomaticTVObservationRequest{Lease: lease, OperationID: payload.OperationID, TaskID: payload.TaskID}
			err = runAutomaticTVLeased(ctx, 2*time.Minute, 10*time.Second, func(work context.Context) error {
				return queue.RenewLease(work, job.ID, owner, job.LeaseToken, 30*time.Second)
			}, func(work context.Context) error {
				receipt, observeErr := observer.ObserveAutomaticTV(work, repository, request)
				notifyAutomaticTVOrganization(receipt, observeErr, request.TaskID, organizationWake)
				return observeErr
			})
		}
		if ctx.Err() != nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		current, readErr := q.GetJobByID(cleanup, job.ID)
		if readErr == nil && current.State == "running" && current.LeaseOwner.Valid && current.LeaseOwner.String == owner && current.LeaseToken == job.LeaseToken {
			// Commit owns success and the next stage. A missing commit is never
			// reported as successful by the worker or converted into another Add.
			message := "automatic TV observation not committed"
			if !valid {
				message = "invalid automatic TV observation payload"
			} else if err != nil {
				message = "automatic TV observation needs attention"
			}
			if failErr := queue.Fail(cleanup, job.ID, owner, job.LeaseToken, message, 5*time.Second); failErr != nil {
				a.logger.Debug("automatic TV observation write fenced")
			}
		}
		cancel()
	}
}
