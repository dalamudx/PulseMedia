package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"io/fs"
	"path/filepath"
	"strings"
)

var _ ports.TransferRevisionRepository = (*Repository)(nil)

// The current user's lock precedes receipts and private frozen evidence.
func revisionPermission(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID) (db.TransferPlan, error) {
	who, e := recoveryUser(ctx, q, a)
	if e != nil {
		return db.TransferPlan{}, e
	}
	p, e := q.GetTransferPlanByID(ctx, id)
	if e != nil {
		return p, ports.ErrTransferReviewRequired
	}
	rows, e := q.ListRequirementsForOperation(ctx, p.OperationID)
	if e != nil {
		return p, e
	}
	for _, d := range rows {
		if d.ActorType == string(ports.ActorTypeUser) && d.ActorID == who.String() {
			return p, nil
		}
	}
	return p, ports.ErrForbidden
}
func revisionReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, c *ports.TransferRevisionCommand, key string) (*ports.TransferRevisionReceipt, error) {
	if c == nil {
		return nil, nil
	}
	record, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "transfer.revision:" + key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out ports.TransferRevisionReceipt
	if record.RequestHash != recoveryHash(c) || json.Unmarshal(record.ResponseBody, &out) != nil || out.PreviousPlanID != c.PlanID {
		return nil, ErrReviewRequired
	}
	return &out, nil
}
func (r *Repository) PreviewTransferRevision(ctx context.Context, a *ports.Actor, id uuid.UUID, target string) (*ports.TransferRevisionPreview, error) {
	p, _, e := r.revision(ctx, a, id, target, nil, "")
	return p, e
}
func (r *Repository) PreviewTransferActionRevision(ctx context.Context, a *ports.Actor, id uuid.UUID, target, action string) (*ports.TransferRevisionPreview, error) {
	p, _, err := r.revision(ctx, a, id, target, nil, "", action)
	return p, err
}
func (r *Repository) CommitTransferRevision(ctx context.Context, a *ports.Actor, c ports.TransferRevisionCommand, key string) (*ports.TransferRevisionReceipt, error) {
	if !c.Confirmed || len(c.ExpectedInputStamp) != 64 || key == "" || len(key) > 140 {
		return nil, ErrReviewRequired
	}
	_, out, e := r.revision(ctx, a, c.PlanID, c.TargetRel, &c, key, c.Action)
	return out, e
}
func (r *Repository) revision(ctx context.Context, a *ports.Actor, id uuid.UUID, targetRel string, c *ports.TransferRevisionCommand, key string, actions ...string) (preview *ports.TransferRevisionPreview, receipt *ports.TransferRevisionReceipt, err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ErrReviewRequired) {
			err = ports.ErrUnavailable
		}
	}()
	if !naming.ValidManualTarget(targetRel) {
		return nil, nil, ErrReviewRequired
	}
	action := "hardlink"
	if len(actions) > 1 {
		return nil, nil, ErrReviewRequired
	}
	if len(actions) == 1 && actions[0] != "" {
		action = actions[0]
	}
	if action != "hardlink" && !verifiedAction(action) {
		return nil, nil, ErrReviewRequired
	}
	s := r.execution
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	hint, e := revisionPermission(ctx, q, a, id)
	if e != nil {
		return nil, nil, e
	}
	cached, e := revisionReceipt(ctx, q, a, c, key)
	if e != nil {
		return nil, nil, e
	}
	if cached != nil {
		return nil, cached, nil
	}
	steps, e := q.ListStepsForPlan(ctx, id)
	if e != nil {
		return nil, nil, e
	}
	if _, e = prelockEpisodeTransfer(ctx, q, hint.OperationID); e != nil {
		return nil, nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, nil, e
	}
	held := func(reason string) (*ports.TransferRevisionPreview, *ports.TransferRevisionReceipt, error) {
		if c != nil {
			return nil, nil, ErrReviewRequired
		}
		return &ports.TransferRevisionPreview{PlanID: id, TargetRel: targetRel, Reason: reason}, nil, nil
	}
	if len(steps) != 1 || (hint.Strategy != "hardlink" && !verifiedAction(hint.Strategy)) || hint.OldFileID.Valid || !hint.RootPath.Valid || !hint.RootDevice.Valid || !hint.RootInode.Valid {
		return held("unsupported_original_plan")
	}
	step := steps[0]
	var evidence transferTargetEvidence
	if json.Unmarshal(step.TargetEvidence, &evidence) != nil || step.Action != hint.Strategy || step.State == "completed" || !step.SourceRoot.Valid || !step.SourceRel.Valid || !step.TargetRel.Valid || !step.SourceDevice.Valid || !step.SourceInode.Valid {
		return held("unsupported_original_evidence")
	}
	previousAction, actionErr := loadFileAction(ctx, s.queries, hint, step)
	if actionErr != nil {
		return held("unsupported_original_evidence")
	}
	if previousAction != nil && action == "hardlink" {
		return held("verified_action_required")
	}
	mediaType := transferEvidenceMediaType(step.TargetEvidence)
	if mediaType == "movie" && (evidence.UnitKey != "movie" || evidence.EpisodeOperationID != uuid.Nil) {
		return held("unsupported_original_evidence")
	}
	if mediaType == "tv" {
		if evidence.EpisodeOperationID != hint.OperationID || evidence.InventoryTaskID == uuid.Nil || evidence.UnitKey == "movie" {
			return held("unsupported_original_evidence")
		}
		facts := media.RecognizeRelease(filepath.Base(targetRel))
		if facts.MediaType != media.TypeTV || facts.RequiresReview || facts.Confidence < 0.9 || len(facts.UnitKeys) != 1 || facts.UnitKeys[0] != evidence.UnitKey {
			return held("target_episode_not_canonical")
		}
	}
	if action != "hardlink" && mediaType != "movie" {
		return held("unsupported_action_for_media")
	}
	if (action == "hardlink" && targetRel == step.TargetRel.String) || !strings.EqualFold(filepath.Ext(targetRel), filepath.Ext(step.TargetRel.String)) {
		return held("invalid_revision_target")
	}
	roots, release, e := s.files.LockRoots(ctx, hint.RootPath.String, step.SourceRoot.String)
	if e != nil {
		return nil, nil, e
	}
	defer release()
	observed, e := s.queries.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, nil, e
	}
	rootID, e := roots[hint.RootPath.String].Identity(ctx)
	if e != nil || rootID.Device != hint.RootDevice.Int64 || rootID.Inode != hint.RootInode.Int64 {
		return held("root_identity_changed")
	}
	source, e := roots[step.SourceRoot.String].Inspect(ctx, step.SourceRel.String)
	if e != nil || source != frozen(step) {
		return held("source_identity_changed")
	}
	var sourceRoot ports.FileIdentity
	var selected *fileActionEvidence
	nextEvidence := step.TargetEvidence
	if action != "hardlink" {
		cap, ok := s.files.(ports.VerifiedTransferFilesystem)
		if !ok {
			return held("file_action_unavailable")
		}
		sourceRoot, e = roots[step.SourceRoot.String].Identity(ctx)
		if e != nil {
			return held("source_identity_changed")
		}
		actorID, parseErr := uuid.Parse(a.ID)
		if parseErr != nil {
			return nil, nil, ports.ErrForbidden
		}
		selected = &fileActionEvidence{Version: 1, ActorID: actorID, SourceRootDevice: sourceRoot.Device, SourceRootInode: sourceRoot.Inode}
		if previousAction != nil {
			selected.Content = previousAction.Content
		}
		if action == "reflink" {
			available, probeErr := cap.ProbeReflink(ctx, roots[step.SourceRoot.String], step.SourceRel.String, roots[hint.RootPath.String], source, s.idgen.NewUUID())
			if probeErr != nil || !available {
				return held("reflink_unavailable")
			}
		}
		evidence.FileAction = selected
		nextEvidence, e = json.Marshal(evidence)
		if e != nil {
			return nil, nil, e
		}
	}
	// Missing, rather than conflicting or inaccessible, is the only safe revision boundary.
	for _, rel := range []string{step.TargetRel.String, targetRel} {
		if _, e = roots[hint.RootPath.String].Inspect(ctx, rel); !errors.Is(e, fs.ErrNotExist) {
			return held("target_not_absent")
		}
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
	// Resolve names again; an old directory FD alone cannot establish current namespace.
	freshRoot, e := s.files.OpenRoot(ctx, hint.RootPath.String)
	if e != nil {
		return held("root_identity_changed")
	}
	physical, e := freshRoot.Identity(ctx)
	_, me := freshRoot.Inspect(ctx, marker)
	freshRoot.Close()
	if e != nil || me != nil || physical != rootID {
		return held("root_identity_changed")
	}
	freshSource, e := s.files.OpenRoot(ctx, step.SourceRoot.String)
	if e != nil {
		return held("source_identity_changed")
	}
	freshSourceID, sourceRootErr := freshSource.Identity(ctx)
	physicalSource, e := freshSource.Inspect(ctx, step.SourceRel.String)
	freshSource.Close()
	if e != nil || physicalSource != source || (action != "hardlink" && (sourceRootErr != nil || freshSourceID.Device != sourceRoot.Device || freshSourceID.Inode != sourceRoot.Inode)) {
		return held("source_identity_changed")
	}
	tx, e = s.pool.Begin(ctx)
	if e != nil {
		return nil, nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q = s.queries.WithTx(tx)
	if _, e = revisionPermission(ctx, q, a, id); e != nil {
		return nil, nil, e
	}
	cached, e = revisionReceipt(ctx, q, a, c, key)
	if e != nil {
		return nil, nil, e
	}
	if cached != nil {
		return nil, cached, nil
	}
	if _, e = prelockEpisodeTransfer(ctx, q, hint.OperationID); e != nil {
		return held("frozen_versions_changed")
	}
	root, e := q.LockTransferRoot(ctx, hint.RootID)
	if e != nil {
		return nil, nil, e
	}
	if root.MediaType != mediaType || root.Health != "online" || !root.BaselineCompleted || root.Path != hint.RootPath.String || root.Generation != hint.RootGeneration.Int64 || root.MarkerFilename != markerRoot.MarkerFilename {
		return held("root_not_current")
	}
	if e = lockTransferAcquisition(ctx, q, hint.OperationID, step.UnitID, step.TargetEvidence); e != nil {
		return held("frozen_versions_changed")
	}
	task, e := q.LockDownloadTaskByOperation(ctx, hint.OperationID)
	if e != nil {
		return nil, nil, e
	}
	if task.State != "completed" || task.OrganizationState != "review_required" || !task.TransferPlanID.Valid || uuid.UUID(task.TransferPlanID.Bytes) != id {
		return held("task_not_revisable")
	}
	demands, e := q.ListRequirementsForOperation(ctx, hint.OperationID)
	if e != nil {
		return nil, nil, e
	}
	ownedPending := false
	for _, d := range demands {
		if d.ActorType == string(ports.ActorTypeUser) && d.ActorID == a.ID && d.State == "pending" && d.RootID == root.ID && d.UnitID == step.UnitID {
			ownedPending = true
		}
	}
	if !ownedPending {
		return held("own_requirement_not_pending")
	}
	inputs, e := q.GetDownloadOrganizationInputs(ctx, task.ID)
	if e != nil {
		return nil, nil, e
	}
	if mediaType == "movie" && !singleMovieCoverage(inputs) {
		return held("coverage_not_current")
	}
	if len(inputs) == 0 {
		return held("coverage_not_current")
	}
	for _, in := range inputs {
		if in.UnitID != step.UnitID || in.RootID != root.ID || mediaType == "movie" && !validOrganizationInput(in) {
			return held("coverage_not_current")
		}
	}

	jobs, e := q.LockOriginalOrganizationJobs(ctx, task.ID.String())
	if e != nil {
		return nil, nil, e
	}
	for _, job := range jobs {
		if job.State != "dead" && job.State != "succeeded" && job.State != "canceled" {
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
	if (plan.State != "review_required" && plan.State != "failed") || plan.LeaseUntil.Valid && plan.LeaseUntil.Time.After(now.Time) {
		return held("plan_execution_pending")
	}
	current, e := q.ListStepsForPlan(ctx, id)
	if e != nil || len(current) != 1 || recoveryHash(plan) != recoveryHash(hint) || recoveryHash(current[0]) != recoveryHash(step) {
		return held("original_evidence_changed")
	}
	fresh, e := q.IsTransferObservationFresh(ctx, observed)
	if e != nil {
		return nil, nil, e
	}
	if !fresh.Valid || !fresh.Bool {
		return held("file_observation_expired")
	}
	stampInputs := []any{plan, step, task, root, inputs, source, targetRel}
	if action != "hardlink" {
		stampInputs = append(stampInputs, action, selected, selected.Content)
	}
	stamp := recoveryHash(stampInputs)
	preview = &ports.TransferRevisionPreview{PlanID: id, TaskID: task.ID, TargetRel: targetRel, Action: action, OriginalTargetRel: step.TargetRel.String, SourceSize: step.SourceSize, MediaType: mediaType, UnitKey: evidence.UnitKey, InputStamp: stamp, ObservedAt: observed.Time, Revisable: true, Reason: "missing_target_revision"}
	if c == nil {
		return preview, nil, nil
	}
	if c.ExpectedInputStamp != stamp {
		return nil, nil, ErrReviewRequired
	}
	next, e := q.CreateTransferPlan(ctx, db.CreateTransferPlanParams{ID: s.idgen.NewUUID(), OperationID: plan.OperationID, RootID: plan.RootID, State: "approved", Strategy: action, RequiresApproval: true})
	if e != nil {
		return nil, nil, e
	}
	if e = q.FreezeTransferPlan(ctx, db.FreezeTransferPlanParams{ID: next.ID, RootPath: plan.RootPath, RootGeneration: plan.RootGeneration, OldFileID: plan.OldFileID, RootDevice: plan.RootDevice, RootInode: plan.RootInode}); e != nil {
		return nil, nil, e
	}
	nextStep, e := q.CreateTransferStep(ctx, db.CreateTransferStepParams{ID: s.idgen.NewUUID(), PlanID: next.ID, UnitID: step.UnitID, SourcePath: step.SourcePath, TargetPath: filepath.Join(root.Path, targetRel), Action: action, StepOrder: 1, SourceSize: step.SourceSize, SourceMtime: step.SourceMtime})
	if e != nil {
		return nil, nil, e
	}
	if e = q.FreezeTransferStep(ctx, db.FreezeTransferStepParams{ID: nextStep.ID, SourceRoot: step.SourceRoot, SourceRel: step.SourceRel, TargetRel: text(targetRel), SourceDevice: step.SourceDevice, SourceInode: step.SourceInode, QualityProfile: step.QualityProfile, TargetEvidence: nextEvidence}); e != nil {
		return nil, nil, e
	}
	if selected != nil && selected.Content != nil {
		inherited, err := q.InheritVerifiedTransferContent(ctx, db.InheritVerifiedTransferContentParams{ID: nextStep.ID, PreviousStepID: step.ID})
		if err != nil {
			return nil, nil, err
		}
		if inherited != 1 {
			return nil, nil, ErrReviewRequired
		}
	}
	n, e := q.ReplaceReviewedDownloadPlan(ctx, db.ReplaceReviewedDownloadPlanParams{TaskID: task.ID, PreviousPlanID: task.TransferPlanID, NewPlanID: pgtype.UUID{Bytes: next.ID, Valid: true}})
	if e != nil || n != 1 {
		return nil, nil, ErrReviewRequired
	}
	payload, _ := json.Marshal(map[string]any{"task_id": task.ID, "revision_plan_id": next.ID})
	job, e := q.EnqueueJob(ctx, db.EnqueueJobParams{ID: s.idgen.NewUUID(), Kind: "download_organization", Payload: payload, MaxAttempts: 3, NotBefore: now})
	if e != nil {
		return nil, nil, e
	}
	receipt = &ports.TransferRevisionReceipt{PreviousPlanID: id, PlanID: next.ID, TaskID: task.ID, JobID: job.ID, State: "queued_revised_plan"}
	raw, _ := json.Marshal(receipt)
	audit, _ := json.Marshal(map[string]any{"receipt": receipt, "target_rel": targetRel, "action": action, "reason": c.Reason})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "transfer.target_revision", TargetType: "transfer_plan", TargetID: id.String(), Payload: audit}); e != nil {
		return nil, nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "transfer_revision", AggregateID: id.String()}); e != nil {
		return nil, nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "transfer_revision", AggregateID: id.String()})
	if e != nil {
		return nil, nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.idgen.NewUUID(), AggregateType: "transfer_revision", AggregateID: id.String(), Seq: int64(seq), EventType: "transfer.revision_requested", Payload: raw}); e != nil {
		return nil, nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "transfer.revision:" + key, RequestHash: recoveryHash(c), ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: next.ID.String(), Valid: true}}); e != nil {
		return nil, nil, e
	}
	fresh, e = q.IsTransferObservationFresh(ctx, observed)
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
