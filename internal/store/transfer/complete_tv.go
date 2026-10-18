package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"path/filepath"
	"sort"
)

var _ ports.CompleteTVOrganizationRepository = (*Repository)(nil)

func validateCompleteTVTransfer(ctx context.Context, q *db.Queries, op, plan uuid.UUID) error {
	if _, e := postgres.ReadCompleteTVOrganizationInputs(ctx, q, op, uuid.Nil, false); e != nil {
		return e
	}
	valid, e := q.ValidateCompleteTVTransfer(ctx, plan)
	if e != nil {
		return e
	}
	if !valid {
		return ErrReviewRequired
	}
	return nil
}
func completeTVPermission(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.CompleteTVOrganizationCommand) (db.CompleteTvExecutionAuthority, error) {
	who, e := recoveryUser(ctx, q, a)
	if e != nil {
		return db.CompleteTvExecutionAuthority{}, e
	}
	r, e := q.GetCompleteTVExecutionAuthority(ctx, c.OperationID)
	if e != nil {
		return r, completeTVOrganizationReadError(e)
	}
	if who != r.ActorID {
		return r, ports.ErrForbidden
	}
	var frozen ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(r.Snapshot, &frozen) != nil || frozen.SubscriptionID != r.SubscriptionID || frozen.ActorID != r.ActorID {
		return r, ErrReviewRequired
	}
	// Keep account -> policy -> subscription ordering even for cached receipts.
	// Historical material revisions need not remain executable to recover a receipt.
	if _, e = q.LockSearchPolicy(ctx, frozen.PolicyID); e != nil {
		return r, completeTVOrganizationReadError(e)
	}
	sub, e := q.LockSearchSubscription(ctx, r.SubscriptionID)
	if errors.Is(e, pgx.ErrNoRows) {
		return r, ports.ErrForbidden
	}
	if e != nil {
		return r, e
	}
	if sub.UserID != who {
		return r, ports.ErrForbidden
	}
	task, e := q.GetDownloadTaskByOpID(ctx, c.OperationID)
	if e != nil {
		return r, completeTVOrganizationReadError(e)
	}
	if task.ID != c.TaskID {
		return r, ErrReviewRequired
	}
	return r, nil
}
func completeTVOrganizationReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReviewRequired
	}
	return err
}
func completeTVReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.CompleteTVOrganizationCommand, key string) (*ports.CompleteTVOrganizationReceipt, error) {
	record, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "complete-tv.organize:" + key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out ports.CompleteTVOrganizationReceipt
	if record.RequestHash != recoveryHash(c) || json.Unmarshal(record.ResponseBody, &out) != nil || out.TaskID != c.TaskID {
		return nil, ErrReviewRequired
	}
	return &out, nil
}

type completeVideoObservation struct {
	file                      media.TVTorrentUnitFile
	source, root, rel, target string
	identity                  ports.FileIdentity
}

// Complete packages require an explicit root mapping, including identity maps.
// TranslateRemotePath alone intentionally permits local paths for older callers.
func completeTVMappedSource(mapping map[string]string, remote string) (string, error) {
	local := naming.TranslateRemotePath(mapping, remote)
	if !filepath.IsAbs(local) {
		return "", ErrReviewRequired
	}
	for prefix := range mapping {
		if _, e := relative(filepath.Clean(prefix), remote); e == nil {
			return local, nil
		}
	}
	return "", ErrReviewRequired
}
func (r *Repository) ConfirmCompleteTVOrganization(ctx context.Context, a *ports.Actor, c ports.CompleteTVOrganizationCommand, key string) (receipt *ports.CompleteTVOrganizationReceipt, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !c.Confirmed || len(c.ExpectedEvidenceStamp) != 64 || key == "" || len(key) > 120 {
		return nil, ErrReviewRequired
	}
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err != nil && !errors.Is(err, ErrReviewRequired) && !errors.Is(err, ports.ErrUnauthorized) && !errors.Is(err, ports.ErrForbidden) {
			err = ports.ErrUnavailable
		}
	}()
	s := r.execution
	// Account and scope also protect cached receipts; revocation is checked first.
	pre, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = pre.Rollback(context.WithoutCancel(ctx)) }()
	hint, e := completeTVPermission(ctx, s.queries.WithTx(pre), a, c)
	if e != nil {
		return nil, e
	}
	receipt, e = completeTVReceipt(ctx, s.queries.WithTx(pre), a, c, key)
	if e != nil {
		return nil, e
	}
	if e = pre.Commit(ctx); e != nil {
		return nil, e
	}
	if receipt != nil {
		return receipt, nil
	}
	var f ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(hint.Snapshot, &f) != nil {
		return nil, ErrReviewRequired
	}
	prep, e := s.queries.GetCompleteTVPreparation(ctx, c.OperationID)
	if e != nil {
		return nil, completeTVOrganizationReadError(e)
	}
	var proof struct {
		Kind     string                    `json:"kind"`
		SavePath string                    `json:"save_path"`
		Files    []media.TVTorrentUnitFile `json:"files"`
	}
	if json.Unmarshal(prep.Proof, &proof) != nil || proof.Kind != "complete_tv_prepared_v2" || len(proof.Files) < 2 || len(proof.Files) > 1000 {
		return nil, ErrReviewRequired
	}
	root, e := s.queries.GetLibraryRootByID(ctx, f.RootID)
	if e != nil {
		return nil, e
	}
	client, e := s.queries.GetDownloadClientByID(ctx, f.ClientID)
	if e != nil {
		return nil, e
	}
	var mapping map[string]string
	if json.Unmarshal(client.RemotePathMap, &mapping) != nil || len(mapping) == 0 {
		return nil, ErrReviewRequired
	}
	sourceRoots, e := s.queries.ListTransferSourceRoots(ctx, c.OperationID)
	if e != nil {
		return nil, e
	}
	sort.Slice(sourceRoots, func(i, j int) bool { return len(sourceRoots[i]) > len(sourceRoots[j]) })
	observations := make([]completeVideoObservation, 0, len(proof.Files))
	paths := []string{root.Path}
	for _, v := range proof.Files {
		clean, e := naming.CleanRelPath(v.Path)
		if e != nil || clean != v.Path {
			return nil, ErrReviewRequired
		}
		remote := filepath.Join(proof.SavePath, v.Path)
		local, mapErr := completeTVMappedSource(mapping, remote)
		if mapErr != nil {
			return nil, ErrReviewRequired
		}
		obs := completeVideoObservation{file: v, source: local}
		for _, p := range sourceRoots {
			if rel, e := relative(p, local); e == nil {
				obs.root, obs.rel = p, rel
				break
			}
		}
		if obs.root == "" {
			return nil, ErrReviewRequired
		}
		paths = append(paths, obs.root)
		observations = append(observations, obs)
	}
	// Root slots cover every observation, row-lock wait and final commit; no I/O is
	// performed inside a database transaction. Do not release on logical timeout.
	roots, release, e := s.files.LockRoots(ctx, paths...)
	if e != nil {
		return nil, e
	}
	defer release()
	observed, e := s.queries.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	rootIdentity, e := roots[root.Path].Identity(ctx)
	if e != nil {
		return nil, e
	}
	marker := root.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	if _, e = roots[root.Path].Inspect(ctx, marker); e != nil {
		return nil, e
	}
	for i := range observations {
		obs := &observations[i]
		obs.identity, e = roots[obs.root].Inspect(ctx, obs.rel)
		if e != nil {
			return nil, e
		}
		if obs.identity.Size != obs.file.Size || obs.identity.Device != rootIdentity.Device {
			return nil, ErrReviewRequired
		}
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if _, e = completeTVPermission(ctx, q, a, c); e != nil {
		return nil, e
	}
	receipt, e = completeTVReceipt(ctx, q, a, c, key)
	if e != nil {
		return nil, e
	}
	if receipt != nil {
		return receipt, tx.Commit(ctx)
	}
	if handled, e := postgres.PrelockCompleteTVOrganization(ctx, q, c.OperationID); !handled || e != nil {
		if e == nil {
			e = ErrReviewRequired
		}
		return nil, e
	}
	lockedRoot, e := q.LockTransferRoot(ctx, root.ID)
	if e != nil {
		return nil, e
	}
	if lockedRoot.Path != root.Path || lockedRoot.Generation != root.Generation || lockedRoot.MarkerFilename != root.MarkerFilename || lockedRoot.Health != "online" || !lockedRoot.BaselineCompleted {
		return nil, ErrReviewRequired
	}
	inputs, e := postgres.ReadCompleteTVOrganizationInputs(ctx, q, c.OperationID, c.TrackingJobID, true)
	if e != nil {
		return nil, e
	}
	if inputs.Receipt.EvidenceStamp != c.ExpectedEvidenceStamp || inputs.Task.ID != c.TaskID || inputs.SavePath != proof.SavePath || len(inputs.Files) != len(observations) {
		return nil, ErrReviewRequired
	}
	candidate, e := q.GetCandidateByID(ctx, f.CandidateID)
	if e != nil {
		return nil, e
	}
	var profile quality.Profile
	if json.Unmarshal(candidate.QualityProfile, &profile) != nil {
		return nil, ErrReviewRequired
	}
	parent, e := q.GetMediaByID(ctx, f.MediaID)
	if e != nil {
		return nil, e
	}
	plan, e := q.CreateTransferPlan(ctx, db.CreateTransferPlanParams{ID: s.idgen.NewUUID(), OperationID: c.OperationID, RootID: root.ID, State: "approved", RequiresApproval: true, Strategy: "hardlink"})
	if e != nil {
		return nil, e
	}
	if e = q.FreezeTransferPlan(ctx, db.FreezeTransferPlanParams{ID: plan.ID, RootPath: text(root.Path), RootGeneration: integer(root.Generation), RootDevice: integer(rootIdentity.Device), RootInode: integer(rootIdentity.Inode)}); e != nil {
		return nil, e
	}
	allFiles, _ := json.Marshal(inputs.Receipt.Files)
	if e = q.CreateDownloadFileInventory(ctx, db.CreateDownloadFileInventoryParams{TaskID: c.TaskID, OperationID: c.OperationID, ClientID: inputs.Task.ClientID, InfoHash: inputs.Task.InfoHash, TotalBytes: inputs.Task.TotalBytes, ContentPath: inputs.SavePath, ClientStamp: f.ClientStamp, Files: allFiles}); e != nil {
		return nil, e
	}
	steps := make([]db.TransferStep, 0, len(observations))
	for i := range observations {
		obs := &observations[i]
		u, e := q.GetSearchUnit(ctx, obs.file.Unit.UnitID)
		if e != nil {
			return nil, e
		}
		obs.target, e = naming.FormatEpisodeRel(parent.Title, int(parent.Year.Int32), int(u.SeasonNumber.Int32), int(u.EpisodeNumber.Int32), profile, filepath.Ext(obs.source))
		if e != nil {
			return nil, e
		}
		step, e := q.CreateTransferStep(ctx, db.CreateTransferStepParams{ID: s.idgen.NewUUID(), PlanID: plan.ID, UnitID: obs.file.Unit.UnitID, SourcePath: obs.source, TargetPath: filepath.Join(root.Path, obs.target), Action: "hardlink", StepOrder: int32(i + 1), SourceSize: obs.identity.Size, SourceMtime: obs.identity.Mtime})
		if e != nil {
			return nil, e
		}
		steps = append(steps, step)
	}
	baseline, e := q.ReadCompleteTVTransferState(ctx, db.ReadCompleteTVTransferStateParams{OperationID: c.OperationID, PlanID: plan.ID})
	if e != nil {
		return nil, e
	}
	if e = q.CreateCompleteTVTransferEvidence(ctx, db.CreateCompleteTVTransferEvidenceParams{PlanID: plan.ID, TaskID: c.TaskID, OperationID: c.OperationID, TrackingJobID: c.TrackingJobID, TrackingResult: inputs.Tracking.Result, Baseline: baseline}); e != nil {
		return nil, e
	}
	qualityJSON, _ := json.Marshal(profile)
	for i, obs := range observations {
		evidence, _ := json.Marshal(transferTargetEvidence{CompleteTVPlanID: plan.ID, ParserVersion: media.EpisodeParserVersion, UnitID: obs.file.Unit.UnitID, UnitKey: obs.file.Unit.UnitKey, UnitVersion: obs.file.Unit.UnitVersion, MediaID: f.MediaID, MediaVersion: f.MediaVersion, InventoryTaskID: c.TaskID, InventorySourcePath: obs.source, InventorySizeBytes: obs.file.Size})
		if e = q.FreezeTransferStep(ctx, db.FreezeTransferStepParams{ID: steps[i].ID, SourceRoot: text(obs.root), SourceRel: text(obs.rel), TargetRel: text(obs.target), SourceDevice: integer(obs.identity.Device), SourceInode: integer(obs.identity.Inode), QualityProfile: qualityJSON, TargetEvidence: evidence}); e != nil {
			return nil, e
		}
	}
	if _, e = q.QueueCompleteTVOrganization(ctx, db.QueueCompleteTVOrganizationParams{ID: c.TaskID, TransferPlanID: pgtype.UUID{Bytes: plan.ID, Valid: true}}); e != nil {
		return nil, e
	}
	job, e := q.EnqueueDownloadOrganization(ctx, db.EnqueueDownloadOrganizationParams{ID: s.idgen.NewUUID(), TaskID: c.TaskID})
	if e != nil {
		return nil, e
	}
	receipt = &ports.CompleteTVOrganizationReceipt{TaskID: c.TaskID, PlanID: plan.ID, JobID: job.ID, State: "queued"}
	raw, _ := json.Marshal(receipt)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "complete_tv.organization.confirm", TargetType: "transfer_plan", TargetID: plan.ID.String(), Payload: raw}); e != nil {
		return nil, e
	}
	aggregate := db.LockOutboxAggregateParams{AggregateType: "complete_tv_organization", AggregateID: c.OperationID.String()}
	if e = q.LockOutboxAggregate(ctx, aggregate); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams(aggregate))
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.idgen.NewUUID(), AggregateType: aggregate.AggregateType, AggregateID: aggregate.AggregateID, Seq: int64(seq), EventType: "complete_tv.organization.confirmed", Payload: raw}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "complete-tv.organize:" + key, RequestHash: recoveryHash(c), ResponseCode: 202, ResponseBody: raw, ResourceID: text(plan.ID.String())}); e != nil {
		return nil, e
	}
	if e = validateCompleteTVTransfer(ctx, q, c.OperationID, plan.ID); e != nil {
		return nil, e
	}
	fresh, e := q.IsTransferObservationFresh(ctx, observed)
	if e != nil {
		return nil, e
	}
	if !fresh.Valid || !fresh.Bool {
		return nil, ErrReviewRequired
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return receipt, nil
}
