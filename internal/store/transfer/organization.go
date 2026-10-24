package transfer

import (
	"bytes"
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
	"io/fs"
)

type automaticPlan struct {
	lease ports.JobLease
	task  db.DownloadTask
	input db.GetDownloadOrganizationInputsRow
}

func validOrganizationInput(i db.GetDownloadOrganizationInputsRow) bool {
	if i.MediaType != "movie" || !i.Year.Valid || i.Year.Int32 <= 0 || i.Retired || i.Health != "online" || !i.BaselineCompleted || !i.DemandVersionsCurrent {
		return false
	}
	return trustedMovieCoverage(i)
}
func trustedMovieCoverage(i db.GetDownloadOrganizationInputsRow) bool {
	// Historic matched IDs and source hashes freeze inputs, not parser semantics.
	// Never stamp a fresh plan from coverage the current parser cannot prove.
	parsed := media.RecognizeRelease(i.CandidateTitle)
	if parsed.RequiresReview || parsed.MediaType != media.TypeMovie || len(parsed.UnitKeys) != 1 || parsed.UnitKeys[0] != "movie" {
		return false
	}
	if i.AcquisitionSourceStamp != "" && quality.AcquisitionSourceStamp(i.CandidateTitle, i.CandidateHash, i.LinkEncrypted, i.QualityProfile) != i.AcquisitionSourceStamp {
		return false
	}
	if i.MatchedUnitID.Valid {
		return uuid.UUID(i.MatchedUnitID.Bytes) == i.UnitID
	}
	return i.FrozenCoverage && i.AcquisitionSourceStamp != "" && quality.AcquisitionSourceStamp(i.CandidateTitle, i.CandidateHash, i.LinkEncrypted, i.QualityProfile) == i.AcquisitionSourceStamp
}
func sameOrganizationInput(a, b db.GetDownloadOrganizationInputsRow) bool {
	return a.UnitID == b.UnitID && a.RootID == b.RootID && a.MediaID == b.MediaID && a.Title == b.Title && a.Year == b.Year && bytes.Equal(a.QualityProfile, b.QualityProfile)
}
func guardOrganization(ctx context.Context, q *db.Queries, authority []ports.JobLease) error {
	if len(authority) == 0 {
		return nil
	}
	if len(authority) != 1 {
		return ErrExecutionRejected
	}
	a := authority[0]
	if _, err := q.GuardDownloadOrganizationJob(ctx, db.GuardDownloadOrganizationJobParams{ID: a.ID, Owner: a.Owner, Token: a.Token}); err != nil {
		return ErrExecutionRejected
	}
	return nil
}

// Called only after root locking: unit then acquisition, matching the global UoW order.
type transferTargetEvidence struct {
	AutomaticTVOperationID    *uuid.UUID          `json:"automatic_tv_operation_id,omitempty"`
	AutomaticTVInventoryStamp string              `json:"automatic_tv_inventory_stamp,omitempty"`
	CompleteTVPlanID          uuid.UUID           `json:"complete_tv_plan_id,omitempty"`
	ParserVersion             string              `json:"parser_version"`
	UnitID                    uuid.UUID           `json:"unit_id"`
	UnitKey                   string              `json:"unit_key"`
	UnitVersion               int64               `json:"unit_version"`
	MediaID                   uuid.UUID           `json:"media_id"`
	MediaVersion              int64               `json:"media_version"`
	EpisodeOperationID        uuid.UUID           `json:"episode_operation_id,omitempty"`
	InventoryTaskID           uuid.UUID           `json:"inventory_task_id,omitempty"`
	InventorySourcePath       string              `json:"inventory_source_path,omitempty"`
	InventorySizeBytes        int64               `json:"inventory_size_bytes,omitempty"`
	FileAction                *fileActionEvidence `json:"file_action,omitempty"`
	NamingVersion             int64               `json:"naming_version,omitempty"`
}

func lockTransferAcquisition(ctx context.Context, q *db.Queries, op, unit uuid.UUID, evidence []byte) error {
	var frozen transferTargetEvidence
	if len(evidence) == 0 || json.Unmarshal(evidence, &frozen) != nil || frozen.ParserVersion != media.EpisodeParserVersion || frozen.UnitID != unit || (frozen.AutomaticTVOperationID == nil && frozen.EpisodeOperationID == uuid.Nil && frozen.CompleteTVPlanID == uuid.Nil && frozen.UnitKey != "movie") || frozen.UnitVersion < 1 || frozen.MediaID == uuid.Nil || frozen.MediaVersion < 1 {
		return ErrReviewRequired
	}
	if frozen.AutomaticTVOperationID != nil {
		if frozen.EpisodeOperationID != uuid.Nil || frozen.CompleteTVPlanID != uuid.Nil {
			return ErrReviewRequired
		}
		return validateAutomaticTVTransfer(ctx, q, op, frozen)
	}
	if frozen.CompleteTVPlanID != uuid.Nil {
		return validateCompleteTVTransfer(ctx, q, op, frozen.CompleteTVPlanID)
	}
	locked, err := q.LockTransferUnit(ctx, unit)
	if err != nil {
		return err
	}
	if locked.Retired || locked.UnitKey != frozen.UnitKey || locked.MetadataVersion != frozen.UnitVersion || locked.MediaID != frozen.MediaID {
		return ErrReviewRequired
	}
	if err := q.LockDownloadAcquisition(ctx, op); err != nil {
		return err
	}
	if _, err := q.LockDownloadTaskByOperation(ctx, op); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	parent, err := q.LockSubmissionMedia(ctx, locked.MediaID)
	if err != nil {
		return err
	}
	if parent.MediaType != transferEvidenceMediaType(evidence) || parent.ID != frozen.MediaID || parent.MetadataVersion != frozen.MediaVersion {
		return ErrReviewRequired
	}
	if frozen.EpisodeOperationID != uuid.Nil {
		if frozen.EpisodeOperationID != op {
			return ErrReviewRequired
		}
		task, e := q.GetDownloadTaskByOpID(ctx, op)
		if e != nil || task.ID != frozen.InventoryTaskID {
			return ErrReviewRequired
		}
		record, e := q.GetEpisodeExecutionAuthority(ctx, op)
		if e != nil {
			return ErrReviewRequired
		}
		root, e := q.GetLibraryRootByID(ctx, record.RootID)
		if e != nil {
			return ErrReviewRequired
		}
		_, e = validateEpisodeTransfer(ctx, q, op, root, locked, task)
		return e
	}
	return validateMovieInventoryEvidence(ctx, q, op, frozen)
}

// OrganizeDownload consumes durable work; it never calls the downloader Add API.
// Only a single confirmed movie video is supported automatically; ambiguity is review.
func (s *Service) OrganizeDownload(ctx context.Context, lease ports.JobLease) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if err := guardOrganization(ctx, q, []ports.JobLease{lease}); err != nil {
		return err
	}
	job, err := q.GetJobByID(ctx, lease.ID)
	if err != nil {
		return err
	}
	var payload struct {
		TaskID         uuid.UUID `json:"task_id"`
		RevisionPlanID uuid.UUID `json:"revision_plan_id"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.TaskID == uuid.Nil {
		return ErrReviewRequired
	}
	task, err := q.LockDownloadTask(ctx, payload.TaskID)
	if err != nil {
		return err
	}
	if payload.RevisionPlanID != uuid.Nil && (!task.TransferPlanID.Valid || uuid.UUID(task.TransferPlanID.Bytes) != payload.RevisionPlanID) {
		return ErrExecutionRejected
	}
	if task.State != "completed" {
		return ErrReviewRequired
	}
	inputs, err := q.GetDownloadOrganizationInputs(ctx, task.ID)
	if err != nil {
		return err
	}
	client, err := q.GetDownloadClientByID(ctx, task.ClientID)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if handled, e := s.hasAutomaticTVOrganization(ctx, task.OperationID); e != nil {
		return e
	} else if handled {
		return s.organizeAutomaticTVEpisode(ctx, lease, task, client)
	}
	if _, e := s.queries.GetEpisodeExecutionAuthority(ctx, task.OperationID); e == nil {
		return s.organizeReviewedEpisode(ctx, lease, task, client)
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if !task.TransferPlanID.Valid {
		if !singleMovieCoverage(inputs) {
			return s.reviewOrganization(ctx, lease, task.ID)
		}
		selected := -1
		for i := range inputs {
			if validOrganizationInput(inputs[i]) {
				selected = i
				break
			}
		}
		if selected < 0 {
			return s.reviewOrganization(ctx, lease, task.ID)
		}
		inputs[0], inputs[selected] = inputs[selected], inputs[0]
	}
	if task.TransferPlanID.Valid {
		plan, readErr := s.queries.GetTransferPlanByID(ctx, uuid.UUID(task.TransferPlanID.Bytes))
		if readErr != nil {
			return readErr
		}
		if plan.OperationID != task.OperationID {
			err = ErrReviewRequired
		} else {
			err = s.executePlan(ctx, plan.ID, lease)
		}
	} else if task.OrganizationState != "queued" || len(inputs) == 0 || !validOrganizationInput(inputs[0]) || !task.ContentPath.Valid || task.TotalBytes <= 0 || task.Progress != 1 || task.DownloadedBytes < task.TotalBytes {
		err = ErrReviewRequired
	} else {
		var mapping map[string]string
		var profile quality.Profile
		if json.Unmarshal(client.RemotePathMap, &mapping) != nil || json.Unmarshal(inputs[0].QualityProfile, &profile) != nil {
			err = ErrReviewRequired
		} else {
			local := naming.TranslateRemotePath(mapping, task.ContentPath.String)
			auto := &automaticPlan{lease: lease, task: task, input: inputs[0]}
			plan, _, createErr := s.createMoviePlan(ctx, task.OperationID, inputs[0].RootID, inputs[0].UnitID, local, inputs[0].Title, int(inputs[0].Year.Int32), profile, pgtype.UUID{}, auto)
			if createErr != nil {
				err = createErr
			} else {
				err = s.executePlan(ctx, plan.ID, lease)
			}
		}
	}
	if reviewable(err) {
		return s.reviewOrganization(ctx, lease, task.ID)
	}
	return err
}
func singleMovieCoverage(inputs []db.GetDownloadOrganizationInputsRow) bool {
	if len(inputs) == 0 {
		return false
	}
	for _, input := range inputs {
		if input.UnitID != inputs[0].UnitID || input.MediaID != inputs[0].MediaID || input.MediaType != "movie" || input.UnitKey != "movie" || !trustedMovieCoverage(input) {
			return false
		}
	}
	return true
}
func containsOrganizationInput(inputs []db.GetDownloadOrganizationInputsRow, target db.GetDownloadOrganizationInputsRow) bool {
	for _, input := range inputs {
		if sameOrganizationInput(input, target) {
			return true
		}
	}
	return false
}

func reviewable(err error) bool {
	return errors.Is(err, ErrReviewRequired) || errors.Is(err, ports.ErrFileEscape) || errors.Is(err, ports.ErrFileSymlink) || errors.Is(err, ports.ErrFileIdentity) || errors.Is(err, ports.ErrFileConflict) || errors.Is(err, ports.ErrUnavailable) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}
func (s *Service) reviewOrganization(ctx context.Context, lease ports.JobLease, taskID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if err := guardOrganization(ctx, q, []ports.JobLease{lease}); err != nil {
		return err
	}
	if _, err = q.LockDownloadTask(ctx, taskID); err != nil {
		return err
	}
	if _, err = q.ReviewDownloadOrganization(ctx, db.ReviewDownloadOrganizationParams{ID: taskID, OrganizationReason: pgtype.Text{String: "source, mapping, coverage or root requires manual review", Valid: true}}); err != nil {
		return err
	}
	if _, err = q.ReviewDownloadOrganizationJob(ctx, db.ReviewDownloadOrganizationJobParams{ID: lease.ID, Owner: lease.Owner, Token: lease.Token}); err != nil {
		return ErrExecutionRejected
	}
	payload, err := json.Marshal(map[string]any{"task_id": taskID})
	if err != nil {
		return err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: "system", ActorID: lease.Owner, Action: "transfer.review_required", TargetType: "download_task", TargetID: taskID.String(), Payload: payload}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) finishOrganization(ctx context.Context, lease ports.JobLease, plan db.TransferPlan, alreadyDone bool) error {
	if handled, e := s.confirmAutomaticTVOrganization(ctx, lease, plan); handled || e != nil {
		return e
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)
	if err := s.requireFileActionCompletion(ctx, q, plan); err != nil {
		return err
	}
	full := false
	var fullJob db.Job
	if _, e := q.GetCompleteTVTransferEvidence(ctx, plan.OperationID); e == nil {
		full = true
		if _, e = postgres.PrelockCompleteTVOrganization(ctx, q, plan.OperationID); e != nil {
			return e
		}
		root, e := q.LockTransferRoot(ctx, plan.RootID)
		if e != nil {
			return e
		}
		if root.Path != plan.RootPath.String || root.Generation != plan.RootGeneration.Int64 || root.Health != "online" || !root.BaselineCompleted {
			return ErrReviewRequired
		}
		if _, e = postgres.ReadCompleteTVOrganizationInputs(ctx, q, plan.OperationID, uuid.Nil, false); e != nil {
			return e
		}
		if e = validateCompleteTVTransfer(ctx, q, plan.OperationID, plan.ID); e != nil {
			return e
		}
		fullJob, e = q.LockCompleteTVOrganizationTracking(ctx, lease.ID)
		if e != nil {
			return e
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if err := guardOrganization(ctx, q, []ports.JobLease{lease}); err != nil {
		return err
	}
	if !alreadyDone {
		if _, err := q.GuardTransferPlan(ctx, guardParams(plan)); err != nil {
			return ErrExecutionRejected
		}
	}
	hint, err := q.GetDownloadTaskByOpID(ctx, plan.OperationID)
	if err != nil {
		return err
	}
	if _, err = q.LockDownloadTaskUnits(ctx, hint.ID); err != nil {
		return err
	}
	if err = q.LockDownloadAcquisition(ctx, plan.OperationID); err != nil {
		return err
	}
	task, err := q.LockDownloadTaskByOperation(ctx, plan.OperationID)
	if err != nil {
		return err
	}
	if !task.TransferPlanID.Valid || uuid.UUID(task.TransferPlanID.Bytes) != plan.ID {
		return ErrReviewRequired
	}
	pending, err := q.CountPendingOrganizationRequirements(ctx, plan.OperationID)
	if err != nil {
		return err
	}

	if !alreadyDone {
		if _, err = q.FinishTransferPlan(ctx, db.FinishTransferPlanParams{ID: plan.ID, Owner: plan.ExecutionOwner.String, Token: plan.ExecutionToken}); err != nil {
			return ErrExecutionRejected
		}
	} else {
		persisted, err := q.GetTransferPlanByID(ctx, plan.ID)
		if err != nil {
			return err
		}
		if persisted.State != "completed" {
			return ErrExecutionRejected
		}
	}
	if pending > 0 {
		if _, err = q.ContinueDownloadOrganization(ctx, db.ContinueDownloadOrganizationParams{ID: task.ID, TransferPlanID: task.TransferPlanID}); err != nil {
			return err
		}
		if _, err = q.ContinueOrganizationJob(ctx, db.ContinueOrganizationJobParams{ID: lease.ID, Owner: lease.Owner, Token: lease.Token, PlanID: plan.ID}); err != nil {
			return ErrExecutionRejected
		}
		return tx.Commit(ctx)
	}
	if task.OrganizationState != "completed" {
		if _, err = q.FinishDownloadOrganization(ctx, db.FinishDownloadOrganizationParams{ID: task.ID, TransferPlanID: task.TransferPlanID}); err != nil {
			return err
		}
	}
	if err = q.ReleaseCancelledOrganizationReservations(ctx, plan.OperationID); err != nil {
		return err
	}
	if full {
		err = q.ReleaseCompleteTVOrganizationLanes(ctx, plan.OperationID)
	} else {
		err = q.ReleaseFinishedAcquisitionLane(ctx, plan.OperationID)
	}
	if err != nil {
		return err
	}
	result, err := json.Marshal(map[string]any{"task_id": task.ID, "plan_id": plan.ID, "state": "completed"})
	if err != nil {
		return err
	}
	if _, err = q.FinishJob(ctx, db.FinishJobParams{Column1: lease.ID, Column2: lease.Owner, Column3: lease.Token, Column4: result}); err != nil {
		return ErrExecutionRejected
	}
	if full {
		if _, e := postgres.ReadCompleteTVOrganizationInputs(ctx, q, plan.OperationID, uuid.Nil, false); e != nil {
			return e
		}
		if e := validateCompleteTVTransfer(ctx, q, plan.OperationID, plan.ID); e != nil {
			return e
		}
		persisted, e := q.GetTransferPlanByID(ctx, plan.ID)
		if e != nil {
			return e
		}
		if persisted.State != "completed" {
			return ErrExecutionRejected
		}
		finalJob, e := q.LockCompleteTVOrganizationTracking(ctx, lease.ID)
		if e != nil {
			return e
		}
		var receipt struct {
			TaskID uuid.UUID `json:"task_id"`
			PlanID uuid.UUID `json:"plan_id"`
			State  string    `json:"state"`
		}
		if finalJob.Kind != "download_organization" || finalJob.State != "succeeded" || finalJob.LeaseToken != lease.Token || !bytes.Equal(finalJob.Payload, fullJob.Payload) || json.Unmarshal(finalJob.Result, &receipt) != nil || receipt.TaskID != task.ID || receipt.PlanID != plan.ID || receipt.State != "completed" {
			return ErrExecutionRejected
		}
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return e
		}
		if !fullJob.LeaseUntil.Valid || !now.Time.Before(fullJob.LeaseUntil.Time) {
			return ErrExecutionRejected
		}
	}
	return tx.Commit(ctx)
}
