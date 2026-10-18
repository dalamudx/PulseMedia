package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"io/fs"
)

var _ ports.TransferRecoveryRepository = (*Repository)(nil)

func recoveryUser(ctx context.Context, q *db.Queries, a *ports.Actor) (uuid.UUID, error) {
	if a == nil {
		return uuid.Nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return uuid.Nil, ports.ErrForbidden
	}
	id, e := uuid.Parse(a.ID)
	if e != nil {
		return uuid.Nil, ports.ErrForbidden
	}
	u, e := q.LockAcquisitionCommandUser(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return uuid.Nil, ports.ErrForbidden
	}
	if e != nil {
		return uuid.Nil, e
	}
	if u.Role != ports.RoleAdmin || !u.IsActive {
		return uuid.Nil, ports.ErrForbidden
	}
	return id, nil
}
func recoveryHash(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func recoveryPermission(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID) (db.TransferPlan, error) {
	who, e := recoveryUser(ctx, q, a)
	if e != nil {
		return db.TransferPlan{}, e
	}
	p, e := q.GetTransferPlanByID(ctx, id)
	if e != nil {
		return p, ports.ErrTransferReviewRequired
	}
	r, e := q.GetEpisodeExecutionAuthority(ctx, p.OperationID)
	if e != nil || r.ActorID != who {
		return p, ports.ErrForbidden
	}
	return p, nil
}

// Recovery input and a cached receipt share the current-user lock. This short
// transaction ends before directory capabilities or filesystem observations.
func (r *Repository) recoveryInput(ctx context.Context, a *ports.Actor, id uuid.UUID, hash, nonce string) (db.TransferPlan, *ports.TransferRecoveryReceipt, error) {
	tx, err := r.execution.pool.Begin(ctx)
	if err != nil {
		return db.TransferPlan{}, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := r.execution.queries.WithTx(tx)
	plan, err := recoveryPermission(ctx, q, a, id)
	if err != nil {
		return plan, nil, err
	}
	var receipt *ports.TransferRecoveryReceipt
	if nonce != "" {
		record, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: nonce})
		if e == nil {
			var result ports.TransferRecoveryReceipt
			if record.RequestHash != hash || json.Unmarshal(record.ResponseBody, &result) != nil || result.PlanID != id {
				return plan, nil, ErrReviewRequired
			}
			receipt = &result
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return plan, nil, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return plan, nil, err
	}
	return plan, receipt, nil
}

func (r *Repository) PreviewOriginalTransferRecovery(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.TransferRecoveryPreview, error) {
	out, _, e := r.recovery(ctx, a, id, nil, "")
	return out, e
}
func (r *Repository) CommitOriginalTransferRecovery(ctx context.Context, a *ports.Actor, c ports.TransferRecoveryCommand, key string) (*ports.TransferRecoveryReceipt, error) {
	if !c.Confirmed || len(c.ExpectedInputStamp) != 64 || key == "" || len(key) > 140 {
		return nil, ErrReviewRequired
	}
	_, out, e := r.recovery(ctx, a, c.PlanID, &c, key)
	return out, e
}
func (r *Repository) recovery(ctx context.Context, a *ports.Actor, id uuid.UUID, c *ports.TransferRecoveryCommand, key string) (preview *ports.TransferRecoveryPreview, receipt *ports.TransferRecoveryReceipt, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ErrReviewRequired) {
			err = ports.ErrUnavailable
		}
	}()
	s := r.execution
	hash, nonce := "", ""
	if c != nil {
		hash, nonce = recoveryHash(c), "transfer.recovery:"+key
	}
	hint, cached, e := r.recoveryInput(ctx, a, id, hash, nonce)
	if e != nil {
		return nil, nil, e
	}
	if cached != nil {
		return nil, cached, nil
	}
	held := func(reason string) (*ports.TransferRecoveryPreview, *ports.TransferRecoveryReceipt, error) {
		if c != nil {
			return nil, nil, ErrReviewRequired
		}
		return &ports.TransferRecoveryPreview{PlanID: id, Reason: reason}, nil, nil
	}
	steps, e := s.queries.ListStepsForPlan(ctx, id)
	if e != nil {
		return nil, nil, e
	}
	if len(steps) != 1 || hint.Strategy != "hardlink" || hint.OldFileID.Valid || !hint.RootPath.Valid || !hint.RootDevice.Valid || !hint.RootInode.Valid {
		return held("unsupported_original_plan")
	}
	step := steps[0]
	if !step.SourceRoot.Valid || !step.SourceRel.Valid || !step.TargetRel.Valid || !step.SourceDevice.Valid || !step.SourceInode.Valid || step.State == "completed" {
		return held("original_evidence_missing")
	}
	roots, release, e := s.files.LockRoots(ctx, hint.RootPath.String, step.SourceRoot.String)
	if e != nil {
		return nil, nil, e
	}
	defer release()
	observedAt, e := s.queries.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, nil, e
	}
	rootIdentity, e := roots[hint.RootPath.String].Identity(ctx)
	if e != nil || rootIdentity.Device != hint.RootDevice.Int64 || rootIdentity.Inode != hint.RootInode.Int64 {
		return held("root_identity_changed")
	}
	source, e := roots[step.SourceRoot.String].Inspect(ctx, step.SourceRel.String)
	if e != nil || source != frozen(step) {
		return held("source_identity_changed")
	}
	target, e := roots[hint.RootPath.String].Inspect(ctx, step.TargetRel.String)
	if errors.Is(e, fs.ErrNotExist) {
		return held("original_link_not_observed")
	}
	if e != nil || target != source {
		return held("target_identity_unconfirmed")
	}
	markerRoot, e := s.queries.GetLibraryRootByID(ctx, hint.RootID)
	if e != nil {
		return nil, nil, e
	}
	marker := markerRoot.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	if _, e = roots[hint.RootPath.String].Inspect(ctx, marker); e != nil {
		return held("root_identity_changed")
	}
	if e = s.checkStepNamespace(ctx, hint, step, marker); e != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return held("root_identity_changed")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if _, e = recoveryPermission(ctx, q, a, id); e != nil {
		return nil, nil, e
	}
	if c != nil {
		old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: nonce})
		if e == nil {
			var out ports.TransferRecoveryReceipt
			if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &out) != nil || out.PlanID != id {
				return nil, nil, ErrReviewRequired
			}
			return nil, &out, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, nil, e
		}
	}
	if _, e = prelockEpisodeTransfer(ctx, q, hint.OperationID); e != nil {
		if errors.Is(e, ErrReviewRequired) {
			return held("frozen_versions_changed")
		}
		return nil, nil, e
	}
	task, e := q.GetDownloadTaskByOpID(ctx, hint.OperationID)
	if e != nil {
		return nil, nil, e
	}
	jobs, e := q.LockOriginalOrganizationJobs(ctx, task.ID.String())
	if e != nil {
		return nil, nil, e
	}
	for _, j := range jobs {
		if j.State != "succeeded" && j.State != "dead" && j.State != "canceled" {
			return held("organization_execution_pending")
		}
	}
	plan, e := q.LockOriginalRecoveryPlan(ctx, id)
	if e != nil {
		return nil, nil, e
	}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, nil, e
	}
	if plan.State != "review_required" && plan.State != "failed" && plan.State != "executing" {
		return held("plan_not_recoverable")
	}
	if plan.LeaseUntil.Valid && plan.LeaseUntil.Time.After(now.Time) {
		return held("execution_lease_active")
	}
	root, e := q.LockTransferRoot(ctx, plan.RootID)
	if e != nil {
		return nil, nil, e
	}
	if root.Path != plan.RootPath.String || root.Generation != plan.RootGeneration.Int64 || root.Health != "online" || !root.BaselineCompleted || root.MarkerFilename != markerRoot.MarkerFilename {
		return held("root_not_current")
	}

	if e = lockTransferAcquisition(ctx, q, plan.OperationID, step.UnitID, step.TargetEvidence); e != nil {
		if errors.Is(e, ErrReviewRequired) || errors.Is(e, ports.ErrDownloadDemandChanged) {
			return held("frozen_versions_changed")
		}
		return nil, nil, e
	}
	task, e = q.LockDownloadTaskByOperation(ctx, plan.OperationID)
	if e != nil {
		return nil, nil, e
	}
	if !task.TransferPlanID.Valid || uuid.UUID(task.TransferPlanID.Bytes) != id || task.OrganizationState != "review_required" {
		return held("task_not_recoverable")
	}
	current, e := q.ListStepsForPlan(ctx, id)
	if e != nil || len(current) != 1 || recoveryHash(current[0]) != recoveryHash(step) {
		return held("original_evidence_changed")
	}
	authority, e := q.GetEpisodeExecutionAuthority(ctx, plan.OperationID)
	if e != nil {
		return nil, nil, e
	}
	fresh, e := q.IsTransferObservationFresh(ctx, observedAt)
	if e != nil {
		return nil, nil, e
	}
	if !fresh.Valid || !fresh.Bool {
		return held("file_observation_expired")
	}
	stamp := recoveryHash([]any{plan, current[0], task, authority.Snapshot, source, target})
	preview = &ports.TransferRecoveryPreview{PlanID: id, TaskID: task.ID, InputStamp: stamp, ObservedAt: observedAt.Time, Recoverable: true, Reason: "original_hardlink_confirmed"}
	if c == nil {
		return preview, nil, nil
	}
	if stamp != c.ExpectedInputStamp {
		return nil, nil, ErrReviewRequired
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: nonce})
	if e == nil {
		var out ports.TransferRecoveryReceipt
		if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &out) != nil || out.PlanID != id {
			return nil, nil, ErrReviewRequired
		}
		return nil, &out, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, nil, e
	}
	if n, e := q.ApproveOriginalRecoveryPlan(ctx, id); e != nil || n != 1 {
		return nil, nil, ErrReviewRequired
	}
	if n, e := q.QueueOriginalRecoveryTask(ctx, db.QueueOriginalRecoveryTaskParams{ID: task.ID, TransferPlanID: pgtype.UUID{Bytes: id, Valid: true}}); e != nil || n != 1 {
		return nil, nil, ErrReviewRequired
	}
	payload, _ := json.Marshal(map[string]any{"task_id": task.ID, "recovery_plan_id": id})
	job, e := q.EnqueueJob(ctx, db.EnqueueJobParams{ID: s.idgen.NewUUID(), Kind: "download_organization", Payload: payload, MaxAttempts: 3, NotBefore: now})
	if e != nil {
		return nil, nil, e
	}
	receipt = &ports.TransferRecoveryReceipt{PlanID: id, TaskID: task.ID, JobID: job.ID, State: "queued_original_plan"}
	raw, _ := json.Marshal(receipt)
	audit, _ := json.Marshal(map[string]any{"receipt": receipt, "reason": c.Reason})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "transfer.original_recovery", TargetType: "transfer_plan", TargetID: id.String(), Payload: audit}); e != nil {
		return nil, nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "transfer_recovery", AggregateID: id.String()}); e != nil {
		return nil, nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "transfer_recovery", AggregateID: id.String()})
	if e != nil {
		return nil, nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.idgen.NewUUID(), AggregateType: "transfer_recovery", AggregateID: id.String(), Seq: int64(seq), EventType: "transfer.recovery_requested", Payload: raw}); e != nil {
		return nil, nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: nonce, RequestHash: hash, ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: id.String(), Valid: true}}); e != nil {
		return nil, nil, e
	}
	if e = postgres.GuardEpisodeNumberingTime(ctx, q, plan.OperationID); e != nil {
		return nil, nil, ErrReviewRequired
	}
	fresh, e = q.IsTransferObservationFresh(ctx, observedAt)
	if e != nil {
		return nil, nil, e
	}
	if !fresh.Valid || !fresh.Bool {
		return nil, nil, ErrReviewRequired
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, nil, e
	}
	return preview, receipt, nil
}
