package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type UntrackedOrganizationLocked struct {
	Operation    ports.UntrackedOrganizationOperation
	Confirmation ports.UntrackedConfirmationReceipt
	Units        []UntrackedOrganizationUnit
	Plan         db.TransferPlan
	Step         db.TransferStep
}

func organizationEvidence(m ports.UntrackedOrganizationMaterial, u untrackedOrganizationUnitRow) []byte {
	o, c := m.Operation, m.Confirmation.Material
	return reviewJSON(map[string]any{
		"untracked_organization_id": o.ID, "confirmation_id": o.ConfirmationID,
		"scope_id": o.ScopeID, "scope_version": o.ScopeVersion, "file_id": o.FileID, "file_revision": o.FileRevision,
		"material_hash": o.MaterialHash, "media_type": c.Scope.TargetMediaType,
		"media_id": u.MediaID, "unit_id": u.UnitID, "unit_key": u.UnitKey,
		"media_version": u.MediaMetadataVersion, "unit_version": u.MetadataVersion, "parser_version": media.EpisodeParserVersion,
		"source_root": c.Scope.LocalRoot, "source_root_identity": c.Scope.SourceIdentity,
		"source_rel": c.File.RelativePath, "source_identity": c.File.Identity,
		"root_id": c.Scope.TargetRootID, "root_path": c.Scope.TargetRootPath, "root_generation": c.Scope.TargetRootGeneration,
		"root_device": c.Scope.TargetIdentity.Device, "root_inode": c.Scope.TargetIdentity.Inode,
		"baseline_scan_id": c.Scope.TargetBaselineScanID, "marker_filename": c.Scope.TargetMarker,
		"target_rel": c.Input.TargetRelativePath,
	})
}

func organizationPlan(ctx context.Context, tx pgx.Tx, r untrackedOrganizationRow, units []untrackedOrganizationUnitRow, lock bool) (db.TransferPlan, db.TransferStep, error) {
	q := db.New(tx)
	o, c := r.Material.Operation, r.Material.Confirmation.Material
	var p db.TransferPlan
	var step db.TransferStep
	if lock {
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM transfer_plan WHERE id=$1 FOR UPDATE`, o.PlanID).Scan(&id); err != nil {
			return p, step, err
		}
	}
	p, err := q.GetTransferPlanByID(ctx, o.PlanID)
	if err != nil {
		return p, step, err
	}
	steps, err := q.ListStepsForPlan(ctx, o.PlanID)
	if err != nil {
		return p, step, err
	}
	if len(steps) != 1 {
		return p, step, ports.ErrUntrackedOrganizationStale
	}
	step = steps[0]
	if lock {
		step, err = q.LockVerifiedTransferStep(ctx, step.ID)
		if err != nil {
			return p, step, err
		}
	}
	var primary *untrackedOrganizationUnitRow
	for i := range units {
		if units[i].UnitKey == c.Review.Evidence.Candidate.Units[0].UnitKey {
			primary = &units[i]
			break
		}
	}
	if primary == nil || p.ID != o.PlanID || p.OperationID != o.ID || p.RootID != c.Scope.TargetRootID || !p.RequiresApproval || p.Strategy != c.Input.Action ||
		!p.RootPath.Valid || p.RootPath.String != c.Scope.TargetRootPath || !p.RootGeneration.Valid || p.RootGeneration.Int64 != c.Scope.TargetRootGeneration ||
		!p.RootDevice.Valid || p.RootDevice.Int64 != c.Scope.TargetIdentity.Device || !p.RootInode.Valid || p.RootInode.Int64 != c.Scope.TargetIdentity.Inode || p.OldFileID.Valid ||
		!p.CreatedAt.Valid || !p.CreatedAt.Time.Equal(o.CreatedAt) || step.ID != o.StepID || step.PlanID != o.PlanID || step.UnitID != primary.UnitID || step.StepOrder != 0 || step.Action != c.Input.Action ||
		step.SourcePath != path.Join(c.Scope.LocalRoot, c.File.RelativePath) || step.TargetPath != path.Join(c.Scope.TargetRootPath, c.Input.TargetRelativePath) ||
		!step.SourceRoot.Valid || step.SourceRoot.String != c.Scope.LocalRoot || !step.SourceRel.Valid || step.SourceRel.String != c.File.RelativePath ||
		!step.TargetRel.Valid || step.TargetRel.String != c.Input.TargetRelativePath || step.SourceSize != c.File.Identity.Size || step.SourceMtime != c.File.Identity.Mtime ||
		!step.SourceDevice.Valid || step.SourceDevice.Int64 != c.File.Identity.Device || !step.SourceInode.Valid || step.SourceInode.Int64 != c.File.Identity.Inode ||
		!step.CreatedAt.Valid || !step.CreatedAt.Time.Equal(o.CreatedAt) || !reviewJSONSame(step.QualityProfile, organizationQuality(r.Material)) ||
		!reviewJSONSame(step.TargetEvidence, organizationEvidence(r.Material, *primary)) {
		return p, step, ports.ErrUntrackedOrganizationStale
	}
	return p, step, nil
}

func organizationJob(j db.Job, o ports.UntrackedOrganizationOperation, l ports.JobLease, state string) error {
	id, err := organizationPayload(j.Payload)
	if err != nil {
		return err
	}
	if j.ID != o.JobID || id != o.ID || j.Kind != ports.UntrackedOrganizationJobKind || j.State != state || j.MaxAttempts != 3 {
		return ports.ErrUntrackedOrganizationStale
	}
	if state == "queued" {
		if j.Attempts != 0 || j.LeaseToken != 0 || j.LeaseOwner.Valid || j.LeaseUntil.Valid || j.FinishedAt.Valid || j.Error.Valid {
			return ports.ErrUntrackedOrganizationStale
		}
	} else if j.ID != l.ID || j.Attempts < 1 || !j.LeaseOwner.Valid || j.LeaseOwner.String != l.Owner || j.LeaseToken != l.Token || !j.LeaseUntil.Valid {
		return ports.ErrUntrackedOrganizationStale
	}
	if state == "succeeded" && (!j.FinishedAt.Valid || j.Error.Valid) {
		return ports.ErrUntrackedOrganizationStale
	}
	return nil
}

type organizationPermit struct {
	OperationID    uuid.UUID `json:"operation_id"`
	JobID          uuid.UUID `json:"job_id"`
	JobOwner       string    `json:"job_owner"`
	JobToken       int64     `json:"job_token"`
	ExecutionOwner string    `json:"execution_owner"`
	ExecutionToken int64     `json:"execution_token"`
	ContentSize    int64     `json:"content_size"`
	ContentSHA256  string    `json:"content_sha256"`
	ProofHash      string    `json:"proof_hash"`
	IssuedAt       time.Time `json:"issued_at"`
}

func organizationPermitCurrent(ctx context.Context, q *db.Queries, r untrackedOrganizationRow, p db.TransferPlan, l ports.JobLease, terminal bool) error {
	raw, err := q.GetUntrackedOrganizationPermit(ctx, r.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if terminal {
			return ports.ErrUntrackedOrganizationStale
		}
		return nil
	}
	if err != nil {
		return err
	}
	var v organizationPermit
	if checkinDecode(raw, &v) != nil {
		return ports.ErrUntrackedOrganizationStale
	}
	digest, e := hex.DecodeString(strings.TrimPrefix(v.ContentSHA256, `\x`))
	if e != nil || len(digest) != 32 || !strings.HasPrefix(v.ContentSHA256, `\x`) || !reviewHashValid(v.ProofHash) || v.ContentSize != r.Material.Confirmation.Material.File.Identity.Size ||
		v.OperationID != r.ID || v.JobID != l.ID || v.JobID != r.JobID || v.JobOwner == "" || v.JobToken < 1 ||
		!p.ExecutionOwner.Valid || p.ExecutionOwner.String != v.ExecutionOwner || p.ExecutionToken != v.ExecutionToken || v.ExecutionToken < 1 || v.IssuedAt.IsZero() {
		return ports.ErrUntrackedOrganizationStale
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return err
	}
	if v.IssuedAt.Before(r.Material.Operation.CreatedAt) || v.IssuedAt.After(now.Time) {
		return ports.ErrUntrackedOrganizationStale
	}
	evidence, err := q.GetVerifiedTransferContent(ctx, r.StepID)
	if err != nil {
		return err
	}
	var content ports.TransferContent
	if checkinDecode(evidence, &content) != nil || content.File != r.Material.Confirmation.Material.File.Identity || !bytes.Equal(content.SHA256[:], digest) {
		return ports.ErrUntrackedOrganizationStale
	}
	if !terminal {
		return nil
	}
	completion, err := q.GetUntrackedOrganizationCompletion(ctx, r.ID)
	if err != nil {
		return err
	}
	var c struct {
		OperationID   uuid.UUID      `json:"operation_id"`
		StepID        uuid.UUID      `json:"step_id"`
		FileID        uuid.UUID      `json:"file_id"`
		ObservationID uuid.UUID      `json:"observation_id"`
		Result        map[string]any `json:"result"`
		RecordedAt    time.Time      `json:"recorded_at"`
	}
	if checkinDecode(completion, &c) != nil || c.OperationID != r.ID || c.StepID != r.StepID || c.FileID == uuid.Nil || c.ObservationID == uuid.Nil || c.RecordedAt.Before(v.IssuedAt) || c.RecordedAt.After(now.Time) {
		return ports.ErrUntrackedOrganizationStale
	}
	return nil
}

// Caller-owned transaction only. Lock order is isolation -> current account ->
// client -> overlapping stable-ID roots -> scope/file -> operation -> identity,
// canonical media/units -> plan/step -> real job. Reuse before and after tails.
func GuardUntrackedOrganization(ctx context.Context, tx pgx.Tx, operationID uuid.UUID, lease ports.JobLease, terminal bool) (out *UntrackedOrganizationLocked, err error) {
	defer func() { err = organizationError(ctx, err) }()
	if tx == nil || operationID == uuid.Nil || lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrRejected
	}
	q := db.New(tx)
	// Initial immutable read discovers the owner without acquiring business locks.
	initial, err := organizationLoad(ctx, q, operationID, false)
	if err != nil {
		return nil, err
	}
	if initial.JobID != lease.ID {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	if err = organizationCurrent(ctx, tx, initial.Material); err != nil {
		return nil, err
	}
	r, err := organizationLoad(ctx, q, operationID, true)
	if err != nil {
		return nil, err
	}
	if !reviewJSONSame(reviewJSON(r), reviewJSON(initial)) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	units, err := organizationFrozenUnits(ctx, q, r.Material)
	if err != nil {
		return nil, err
	}
	if err = organizationValidateUnits(ctx, q, r.Material, units, true); err != nil {
		return nil, err
	}
	p, step, err := organizationPlan(ctx, tx, r, units, true)
	if err != nil {
		return nil, err
	}
	if _, err = q.LockJobForWrite(ctx, lease.ID); err != nil {
		return nil, err
	}
	j, err := q.GetJobByID(ctx, lease.ID)
	if err != nil {
		return nil, err
	}
	state := "running"
	if terminal {
		state = "succeeded"
	}
	if err = organizationJob(j, r.Material.Operation, lease, state); err != nil {
		return nil, err
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return nil, err
	}
	if !j.LeaseUntil.Valid || !now.Time.Before(j.LeaseUntil.Time) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	if terminal {
		if p.State != "completed" || step.State != "completed" || !step.TargetFileID.Valid {
			return nil, ports.ErrUntrackedOrganizationStale
		}
	} else if (p.State != "approved" && p.State != "executing" && p.State != "review_required") || (step.State != "pending" && step.State != "running" && step.State != "failed" && step.State != "review_required") {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	if err = organizationPermitCurrent(ctx, q, r, p, lease, terminal); err != nil {
		return nil, err
	}
	if terminal {
		if err = organizationCompletionCurrent(ctx, q, r, p, step, lease); err != nil {
			return nil, err
		}
	}
	out = &UntrackedOrganizationLocked{Operation: r.Material.Operation, Confirmation: reviewClone(r.Material.Confirmation), Plan: p, Step: step, Units: make([]UntrackedOrganizationUnit, 0, len(units))}
	for _, u := range units {
		out.Units = append(out.Units, UntrackedOrganizationUnit{MediaID: u.MediaID, UnitID: u.UnitID, MetadataVersion: u.MetadataVersion, UnitKey: u.UnitKey, TMDBEpisodeID: u.TMDBEpisodeID})
	}
	return out, nil
}

// Returns a detached frozen frame for filesystem work OUTSIDE the transaction.
// This method performs database reads/locks only and never grants physical IO.
func (s *UntrackedOrganizationStore) LoadUntrackedOrganizationExecution(ctx context.Context, lease ports.JobLease) (out *UntrackedOrganizationLocked, err error) {
	defer func() { err = organizationError(ctx, err) }()
	if s == nil || s.pool == nil || lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ports.ErrRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	j, err := q.GetAuthorizedSearchJob(ctx, checkinLease(lease, ports.UntrackedOrganizationJobKind))
	if err != nil {
		return nil, err
	}
	id, err := organizationPayload(j.Payload)
	if err != nil {
		return nil, err
	}
	out, err = GuardUntrackedOrganization(ctx, tx, id, lease, false)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func organizationCompletionCurrent(ctx context.Context, q *db.Queries, r untrackedOrganizationRow, p db.TransferPlan, step db.TransferStep, lease ports.JobLease) error {
	raw, err := q.GetUntrackedOrganizationCompletion(ctx, r.ID)
	if err != nil {
		return err
	}
	var c struct {
		OperationID   uuid.UUID                    `json:"operation_id"`
		StepID        uuid.UUID                    `json:"step_id"`
		FileID        uuid.UUID                    `json:"file_id"`
		ObservationID uuid.UUID                    `json:"observation_id"`
		Result        ports.VerifiedTransferResult `json:"result"`
		RecordedAt    time.Time                    `json:"recorded_at"`
	}
	if checkinDecode(raw, &c) != nil || c.OperationID != r.ID || c.StepID != step.ID || !step.TargetFileID.Valid || c.FileID != uuid.UUID(step.TargetFileID.Bytes) {
		return ports.ErrUntrackedOrganizationStale
	}
	observed, err := q.GetUntrackedOrganizationObservation(ctx, c.ObservationID)
	if err != nil {
		return err
	}
	var v struct {
		ID          uuid.UUID                    `json:"id"`
		OperationID uuid.UUID                    `json:"operation_id"`
		JobID       uuid.UUID                    `json:"job_id"`
		JobOwner    string                       `json:"job_owner"`
		JobToken    int64                        `json:"job_token"`
		Result      ports.VerifiedTransferResult `json:"result"`
		RecordedAt  time.Time                    `json:"recorded_at"`
	}
	if checkinDecode(observed, &v) != nil || v.ID != c.ObservationID || v.OperationID != r.ID || v.JobID != lease.ID || v.JobOwner != lease.Owner || v.JobToken != lease.Token || v.RecordedAt.After(c.RecordedAt) || !reviewJSONSame(reviewJSON(v.Result), reviewJSON(c.Result)) {
		return ports.ErrUntrackedOrganizationStale
	}
	evidence, err := q.GetVerifiedTransferContent(ctx, step.ID)
	if err != nil {
		return err
	}
	var content ports.TransferContent
	if checkinDecode(evidence, &content) != nil || content.File != r.Material.Confirmation.Material.File.Identity || c.Result.Target.SHA256 != content.SHA256 || c.Result.Target.File.Size != content.File.Size || c.Result.Target.File.Device < 0 || c.Result.Target.File.Inode <= 0 || c.Result.SourceRemoved != (step.Action == "move") || (step.Action == "hardlink" && c.Result.Target.File != content.File) {
		return ports.ErrUntrackedOrganizationStale
	}
	f, err := q.GetFileByID(ctx, c.FileID)
	if err != nil {
		return err
	}
	if f.RootID != p.RootID || f.RelPath != step.TargetRel.String || f.RetiredAt.Valid || f.SizeBytes != c.Result.Target.File.Size || f.MtimeNs != c.Result.Target.File.Mtime {
		return ports.ErrUntrackedOrganizationStale
	}
	frozen, err := organizationFrozenUnits(ctx, q, r.Material)
	if err != nil {
		return err
	}
	actual, err := q.ListUnitsForFile(ctx, c.FileID)
	if err != nil {
		return err
	}
	if len(actual) != len(frozen) {
		return ports.ErrUntrackedOrganizationStale
	}
	ids := map[uuid.UUID]bool{}
	for _, u := range actual {
		ids[u.ID] = true
	}
	now, err := q.UntrackedDiscoveryClock(ctx)
	if err != nil {
		return err
	}
	for _, u := range frozen {
		if !ids[u.UnitID] {
			return ports.ErrUntrackedOrganizationStale
		}
		facts, e := q.ListActiveFactsForUnit(ctx, u.UnitID)
		if e != nil {
			return e
		}
		found := false
		for _, fact := range facts {
			if fact.Stage != "imported" || !fact.RootID.Valid || uuid.UUID(fact.RootID.Bytes) != p.RootID || (fact.ExpiresAt.Valid && !now.Time.Before(fact.ExpiresAt.Time)) {
				continue
			}
			if !fact.SourceFileID.Valid || uuid.UUID(fact.SourceFileID.Bytes) != c.FileID {
				return ports.ErrUntrackedOrganizationStale
			}
			if reviewJSONSame(fact.QualityProfile, step.QualityProfile) {
				found = true
			}
		}
		if !found {
			return ports.ErrUntrackedOrganizationStale
		}
	}
	return nil
}
