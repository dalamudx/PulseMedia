package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RecoverExpired changes logical delivery only. It never releases an execution
// record or grants another write under an existing permanent permit.
func (s *SeedingScheduleStore) RecoverExpired(ctx context.Context) (err error) {
	defer func() { err = seedingExecutionError(err) }()
	if err = ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = seedingRuntimeGuard(ctx, q); err != nil {
		return err
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "seeding.scheduler", AggregateID: "global"}); err != nil {
		return err
	}
	candidates, err := q.ListRecoverableSeedingJobs(ctx)
	if err != nil {
		return err
	}
	for _, d := range candidates {
		held, e := q.TrySeedingRecoveryGate(ctx, d.ClientID)
		if e != nil {
			return e
		}
		if !held {
			// Rotate checked-but-busy entries so 64 held tasks cannot permanently hide
			// later expired work. No task authority or physical evidence is changed.
			if _, e = q.TouchSeedingRecoverySchedule(ctx, db.TouchSeedingRecoveryScheduleParams{TaskID: d.TaskID, ActiveJobID: pgtype.UUID{Bytes: d.JobID, Valid: true}}); e != nil {
				return e
			}
			continue
		}
		owner, e := q.LockDoubanSyncOwner(ctx, d.OwnerID)
		if e != nil {
			return e
		}
		if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: seedingConfigAction, AggregateID: d.ClientID.String()}); e != nil {
			return e
		}
		if _, e = q.LockDownloadClientForEdit(ctx, d.ClientID); e != nil {
			return e
		}
		config, e := q.LockSeedingConfig(ctx, d.ClientID)
		if e != nil {
			return e
		}
		stamp, e := q.DownloadGlobalRatesBinding(ctx, d.ClientID)
		if e != nil {
			return e
		}
		task, e := q.GetSubmissionTask(ctx, d.TaskID)
		if e != nil {
			return e
		}
		if e = q.LockDownloadAcquisition(ctx, task.OperationID); e != nil {
			return e
		}
		if _, e = q.LockJobForWrite(ctx, d.JobID); e != nil {
			return e
		}
		job, e := q.GetJobByID(ctx, d.JobID)
		if e != nil {
			return e
		}
		schedule, e := q.GetSeedingRemovalSchedule(ctx, d.TaskID)
		if e != nil {
			return e
		}
		if !schedule.ActiveJobID.Valid || schedule.ActiveJobID.Bytes != d.JobID {
			continue
		}
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return e
		}
		if !now.Valid {
			return ports.ErrUnavailable
		}
		if job.State == "running" && (!job.LeaseUntil.Valid || job.LeaseUntil.Time.After(now.Time)) {
			continue
		}
		last, e := q.LatestSeedingExecution(ctx, d.JobID)
		hasExecution := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		_, e = q.UnreleasedSeedingExecution(ctx, d.ClientID)
		unconfirmed := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		permanent, e := q.GetSeedingRemovalPermit(ctx, d.TaskID)
		hasPermit := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		allowed := owner.IsActive && owner.Role == string(ports.RoleAdmin) && config.OwnerID == d.OwnerID && config.ClientStamp == d.ClientStamp && stamp == d.ClientStamp
		if !hasPermit {
			allowed = allowed && config.Enabled && config.Version == d.ConfigVersion
		}
		var payload seedingJobPayload
		bindingOK := job.Kind == "seeding_removal" && job.MaxAttempts == 3 && statsDecode(job.Payload, &payload) == nil && payload == (seedingJobPayload{d.TaskID, d.ClientID, d.ConfigVersion}) && schedule.ConfigVersion == d.ConfigVersion
		if hasPermit {
			bindingOK = bindingOK && permanent.JobID == d.JobID && permanent.ClientID == d.ClientID && permanent.ClientStamp == d.ClientStamp && permanent.OwnerID == d.OwnerID && permanent.ConfigVersion == d.ConfigVersion
		}
		if hasExecution {
			bindingOK = bindingOK && last.ClientID == d.ClientID && last.LeaseToken <= job.LeaseToken && (last.LeaseToken != job.LeaseToken || !last.Result.Valid)
		}
		nextJob, nextSchedule, reason := "queued", "queued", "lease_recovered"
		switch {
		case unconfirmed:
			nextJob = "dead"
			nextSchedule = "review_required"
			reason = "physical_unconfirmed"
		case !bindingOK || hasPermit && !hasExecution:
			nextJob = "dead"
			nextSchedule = "review_required"
			reason = "execution_binding_unconfirmed"
		case job.State == "dead" || job.State == "succeeded" || job.Attempts >= job.MaxAttempts:
			nextJob = "dead"
			nextSchedule = "review_required"
			reason = "attempts_exhausted_or_terminal"
		case !allowed:
			nextJob = "dead"
			nextSchedule = "idle"
			reason = "authority_changed"
			if hasPermit {
				nextSchedule = "review_required"
			}
		default:
			if !hasPermit {
				proof, e := q.ReadOrdinarySeedingProtection(ctx, db.ReadOrdinarySeedingProtectionParams{TaskID: d.TaskID, ClientStamp: d.ClientStamp})
				if e != nil {
					return e
				}
				if !proof.Owned || !proof.Imported || proof.PendingWork {
					nextJob = "dead"
					nextSchedule = "idle"
					reason = "ingestion_protection_changed"
				}
			}
		}
		// Healthy queued work may have become valid again while locks were acquired.
		if job.State == "queued" && nextJob == "queued" {
			continue
		}
		n, e := q.RecoverSeedingRemovalJob(ctx, db.RecoverSeedingRemovalJobParams{JobID: d.JobID, LeaseToken: job.LeaseToken, PreviousState: job.State, NextState: nextJob, Reason: reason})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrSeedingLeaseLost
		}
		n, e = q.RecoverSeedingRemovalSchedule(ctx, db.RecoverSeedingRemovalScheduleParams{TaskID: d.TaskID, JobID: pgtype.UUID{Bytes: d.JobID, Valid: true}, NextState: nextSchedule, IntervalSeconds: config.IntervalSeconds})
		if e != nil {
			return e
		}
		if n != 1 {
			return ports.ErrSeedingLeaseLost
		}
		body, _ := json.Marshal(struct {
			JobID           string `json:"job_id"`
			LeaseToken      int64  `json:"lease_token"`
			Reason          string `json:"reason"`
			PermanentPermit bool   `json:"permanent_permit"`
		}{d.JobID.String(), job.LeaseToken, reason, hasPermit})
		if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(ports.ActorTypeSystem), ActorID: "seeding-recovery", Action: "seeding.removal.recovered", TargetType: "download_task", TargetID: d.TaskID.String(), Payload: body}); e != nil {
			return e
		}
		aggregate := db.LockOutboxAggregateParams{AggregateType: "seeding.removal", AggregateID: d.TaskID.String()}
		if e = q.LockOutboxAggregate(ctx, aggregate); e != nil {
			return e
		}
		seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams(aggregate))
		if e != nil {
			return e
		}
		if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: aggregate.AggregateType, AggregateID: aggregate.AggregateID, EventType: "seeding.removal.recovered", Payload: body, Seq: int64(seq)}); e != nil {
			return e
		}
	}
	if err = seedingRuntimeGuard(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
