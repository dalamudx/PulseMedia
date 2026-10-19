package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type automaticTVSourceExecutor interface {
	ExecuteAutomaticTVSource(context.Context, ports.AutomaticTVSourceTask) error
}

// Source capacity and finalization capacity are separate. Each loop waits for
// its real executor, including I/O that ignores cancellation, before reuse.
func (a *App) runAutomaticTVSearchWorkers(ctx context.Context, queue *jobs.Queue, rounds ports.AutomaticTVRoundRepository, finalizer ports.AutomaticTVAcquisitionRepository, source automaticTVSourceExecutor) {
	var group sync.WaitGroup
	for i := 0; i < 5; i++ {
		group.Add(1)
		go func(finalize bool) {
			defer group.Done()
			a.runAutomaticTVSearchConsumer(ctx, queue, finalizer, source, finalize)
		}(i == 0)
	}
	defer group.Wait()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		maintenance, cancel := context.WithTimeout(ctx, 5*time.Second)
		if _, err := rounds.RecoverAutomaticTVSources(maintenance, 64); err != nil && ctx.Err() == nil {
			a.logger.Error("automatic TV source recovery deferred")
		}
		if _, err := finalizer.RecoverAutomaticTVFinalizers(maintenance, 64); err != nil && ctx.Err() == nil {
			a.logger.Error("automatic TV finalization recovery deferred")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func decodeAutomaticTVPayload(raw []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func (a *App) runAutomaticTVSearchConsumer(ctx context.Context, queue *jobs.Queue, finalizer ports.AutomaticTVAcquisitionRepository, source automaticTVSourceExecutor, finalize bool) {
	owner := a.idgen.NewUUID().String()
	kind := "automatic_tv_source"
	if finalize {
		kind = "automatic_tv_finalize"
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := queue.Claim(ctx, []string{kind}, owner, 30*time.Second)
		if err != nil && ctx.Err() == nil {
			a.logger.Error("automatic TV job claim deferred")
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		lease := ports.JobLease{ID: job.ID, Owner: owner, Token: job.LeaseToken}
		var payload struct {
			RoundID uuid.UUID `json:"round_id"`
		}
		var task ports.AutomaticTVSourceTask
		valid := false
		if finalize {
			valid = decodeAutomaticTVPayload(job.Payload, &payload) && payload.RoundID != uuid.Nil
		} else {
			valid = decodeAutomaticTVPayload(job.Payload, &task) && task.RoundID != uuid.Nil && task.IndexerID != uuid.Nil
			task.Lease = lease
		}
		if !valid {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := queue.Fail(cleanup, job.ID, owner, job.LeaseToken, "invalid automatic TV job payload", time.Second); err != nil && ctx.Err() == nil {
				a.logger.Debug("automatic TV invalid job write fenced")
			}
			cancel()
			continue
		}
		err = runAutomaticTVLeased(ctx, 2*time.Minute, 10*time.Second, func(work context.Context) error {
			return queue.RenewLease(work, job.ID, owner, job.LeaseToken, 30*time.Second)
		}, func(work context.Context) error {
			if finalize {
				_, err := finalizer.FinalizeAutomaticTVRound(work, payload.RoundID, lease)
				return err
			}
			return source.ExecuteAutomaticTVSource(work, task)
		})
		if err != nil && ctx.Err() == nil {
			if finalize {
				// Atomic finalization owns success. A failed or unready R0 attempt
				// may retry the original job; an expired callback cannot rewrite it.
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if failErr := queue.Fail(cleanup, job.ID, owner, job.LeaseToken, "automatic TV decision deferred", 5*time.Second); failErr != nil {
					a.logger.Debug("automatic TV decision write fenced")
				}
				cancel()
			} else {
				// Finish owns the query/source/job transaction. Recovery handles a
				// failed load or interrupted finish after its lease has expired.
				a.logger.Debug("automatic TV source execution fenced")
			}
		}
	}
}
