package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// FIRSTSEND has two physical slots; each consumer joins its executor and heartbeat.
func (a *App) runAutomaticTVSubmissionWorkers(ctx context.Context, queue *jobs.Queue, recovery ports.AutomaticTVSubmissionRecoveryRepository, sender ports.AutomaticTVSendJobExecutor) {
	work, cancel := context.WithCancel(ctx)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); a.runAutomaticTVSubmissionConsumer(work, queue, sender) }()
	}
	defer func() { cancel(); group.Wait() }()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if work.Err() != nil {
			return
		}
		maintenance, stop := context.WithTimeout(work, 5*time.Second)
		_, err := recovery.RecoverAutomaticTVSubmissions(maintenance, 64)
		stop()
		if err != nil && work.Err() == nil {
			a.logger.Error("automatic TV submission recovery deferred")
		}
		select {
		case <-work.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runAutomaticTVSubmissionConsumer(ctx context.Context, queue *jobs.Queue, sender ports.AutomaticTVSendJobExecutor) {
	owner := a.idgen.NewUUID().String()
	q := db.New(a.dbPool)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := queue.Claim(ctx, []string{"automatic_tv_download_submission"}, owner, 30*time.Second)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("automatic TV submission claim deferred")
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		var payload struct {
			OperationID uuid.UUID `json:"operation_id"`
			RoundID     uuid.UUID `json:"round_id"`
			Authority   uuid.UUID `json:"automatic_tv_authority"`
			CandidateID uuid.UUID `json:"candidate_id"`
			RootID      uuid.UUID `json:"root_id"`
			ClientID    uuid.UUID `json:"client_id"`
		}
		// Count keys as well: a missing field must not be accepted as a zero value.
		var keys map[string]json.RawMessage
		valid := decodeAutomaticTVPayload(job.Payload, &payload) && json.Unmarshal(job.Payload, &keys) == nil && len(keys) == 6 &&
			payload.OperationID != uuid.Nil && payload.RoundID != uuid.Nil && payload.Authority == payload.OperationID &&
			payload.CandidateID != uuid.Nil && payload.RootID != uuid.Nil && payload.ClientID != uuid.Nil &&
			job.DedupeKey.Valid && job.DedupeKey.String == payload.OperationID.String()
		if valid {
			request := ports.AutomaticTVSendRequest{OperationID: payload.OperationID, Lease: ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}}
			err = runAutomaticTVLeased(ctx, 2*time.Minute, 10*time.Second, func(work context.Context) error {
				return queue.RenewLease(work, job.ID, owner, job.LeaseToken, 30*time.Second)
			}, func(work context.Context) error { return sender.ExecuteAutomaticTVSendJob(work, request) })
		}
		if ctx.Err() != nil {
			return
		}
		// Prepared work stays running until dedicated recovery fences it after expiry.
		// Queue.Fail would make it claimable again before observation owns its task.
		if valid && errors.Is(err, ports.ErrAutomaticTVNeedsObservation) {
			continue
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		current, readErr := q.GetJobByID(cleanup, job.ID)
		if readErr == nil && current.State == "running" && current.LeaseOwner.Valid && current.LeaseOwner.String == owner && current.LeaseToken == job.LeaseToken {
			// Record owns success, including an unknown physical result. A nil executor
			// result with a still-running job cannot be turned into an unsent success.
			message := "automatic TV submission needs attention"
			if valid && err != nil {
				message = "automatic TV submission requires observation"
			}
			if failErr := queue.Fail(cleanup, job.ID, owner, job.LeaseToken, message, 5*time.Second); failErr != nil && ctx.Err() == nil {
				a.logger.Debug("automatic TV submission write fenced")
			}
		} else if readErr != nil && ctx.Err() == nil {
			a.logger.Debug("automatic TV submission state read deferred")
		}
		cancel()
	}
}
