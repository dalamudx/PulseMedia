package library

import (
	"context"
	"errors"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// UntrackedOrganizationService queues frozen intent; it grants no physical permission.
type UntrackedOrganizationService struct {
	repo  ports.UntrackedOrganizationRepository
	files ports.RootFilesystem
}

func NewUntrackedOrganizationService(repo ports.UntrackedOrganizationRepository, files ports.RootFilesystem) *UntrackedOrganizationService {
	return &UntrackedOrganizationService{repo: repo, files: files}
}

func untrackedOrganizationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ports.ErrUntrackedOrganizationStale) || errors.Is(err, ports.ErrUntrackedReviewStale) {
		return ports.ErrUntrackedOrganizationStale
	}
	return untrackedScopeError(err)
}

func untrackedOrganizationOperationValid(o ports.UntrackedOrganizationOperation, owner string, scopeID, fileID uuid.UUID) bool {
	return o.ID != uuid.Nil && o.ActorID != uuid.Nil && o.ActorID.String() == owner && o.ScopeID == scopeID && o.FileID == fileID && o.ConfirmationID != uuid.Nil && o.PlanID != uuid.Nil && o.StepID != uuid.Nil && o.JobID != uuid.Nil && untrackedReviewVersion(o.ScopeVersion) && untrackedReviewVersion(o.FileRevision) && untrackedReviewHash(o.MaterialHash) && untrackedReviewTime(o.CreatedAt)
}

func untrackedOrganizationReceiptValid(r *ports.UntrackedOrganizationReceipt, owner string, scopeID, fileID uuid.UUID, key string) bool {
	return r != nil && r.CommandKey == key && r.State == "queued" && !r.ExecutionAllowed && untrackedOrganizationOperationValid(r.Operation, owner, scopeID, fileID)
}

func untrackedOrganizationMaterialCopy(m ports.UntrackedOrganizationMaterial) ports.UntrackedOrganizationMaterial {
	m.Confirmation.Material = untrackedReviewMaterialCopy(m.Confirmation.Material)
	return m
}

func untrackedOrganizationOperationEqual(a, b ports.UntrackedOrganizationOperation) bool {
	a.CreatedAt = untrackedReviewInstant(a.CreatedAt)
	b.CreatedAt = untrackedReviewInstant(b.CreatedAt)
	return a == b
}

func untrackedOrganizationMaterialEqual(a, b ports.UntrackedOrganizationMaterial) bool {
	return untrackedOrganizationOperationEqual(a.Operation, b.Operation) && a.Confirmation.CommandKey == b.Confirmation.CommandKey && a.Confirmation.State == b.Confirmation.State && a.Confirmation.ExecutionAllowed == b.Confirmation.ExecutionAllowed && untrackedReviewMaterialEqual(a.Confirmation.Material, b.Confirmation.Material)
}

func untrackedOrganizationMaterialValid(m ports.UntrackedOrganizationMaterial, owner string, scopeID, fileID, confirmationID uuid.UUID) bool {
	o, c := m.Operation, m.Confirmation
	if !untrackedOrganizationOperationValid(o, owner, scopeID, fileID) || o.ConfirmationID != confirmationID || !untrackedScopeKey(c.CommandKey) || !untrackedReviewConfirmationReceiptValid(&c, owner, scopeID, fileID, c.CommandKey) {
		return false
	}
	frozen := c.Material
	return o.ConfirmationID == frozen.ID && o.ScopeVersion == frozen.Scope.Version && o.FileRevision == frozen.File.Revision && o.MaterialHash == untrackedOrganizationMaterialHash(m) && !o.CreatedAt.Before(frozen.CreatedAt)
}

func (s *UntrackedOrganizationService) Organize(ctx context.Context, actor *ports.Actor, scopeID, fileID uuid.UUID, input ports.UntrackedOrganizationInput, key string) (*ports.UntrackedOrganizationReceipt, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, key)
	if err != nil {
		return nil, err
	}
	if input.ConfirmationID == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	p, err := s.repo.PrepareUntrackedOrganization(ctx, &a, scopeID, fileID, input, key)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedOrganizationError(ctx, err)
	}
	if p == nil || (p.Receipt == nil) == (p.Capture == nil) {
		return nil, ports.ErrUnavailable
	}
	if p.Receipt != nil {
		if !untrackedOrganizationReceiptValid(p.Receipt, owner, scopeID, fileID, key) || p.Receipt.Operation.ConfirmationID != input.ConfirmationID {
			return nil, ports.ErrUnavailable
		}
		out := *p.Receipt
		return &out, nil
	}
	m := untrackedOrganizationMaterialCopy(p.Capture.UntrackedOrganizationMaterial())
	if !untrackedOrganizationMaterialValid(m, owner, scopeID, fileID, input.ConfirmationID) {
		return nil, ports.ErrUnavailable
	}
	physical, err := untrackedReviewOpen(ctx, s.files, m.Confirmation.Material.Scope, m.Confirmation.Material.File)
	if err != nil {
		return nil, untrackedOrganizationError(ctx, err)
	}
	defer physical.close()
	if err := physical.check(ctx); err != nil {
		return nil, untrackedOrganizationError(ctx, err)
	}
	if !untrackedOrganizationMaterialEqual(m, p.Capture.UntrackedOrganizationMaterial()) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A commit error may hide a durable queue entry. Recovery uses the original key.
	r, err := s.repo.CommitUntrackedOrganization(ctx, p.Capture)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedOrganizationError(ctx, err)
	}
	if !untrackedOrganizationReceiptValid(r, owner, scopeID, fileID, key) || !untrackedOrganizationOperationEqual(m.Operation, r.Operation) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

func (s *UntrackedOrganizationService) Receipt(ctx context.Context, actor *ports.Actor, scopeID, fileID uuid.UUID, key string) (*ports.UntrackedOrganizationReceipt, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, key)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	r, err := s.repo.ReadUntrackedOrganizationReceipt(ctx, &a, scopeID, fileID, key)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedOrganizationError(ctx, err)
	}
	if !untrackedOrganizationReceiptValid(r, owner, scopeID, fileID, key) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

func (s *UntrackedOrganizationService) Read(ctx context.Context, actor *ports.Actor, scopeID, fileID, operationID uuid.UUID) (*ports.UntrackedOrganizationView, error) {
	a, err := untrackedReviewActor(ctx, actor, scopeID, fileID, "read")
	if err != nil {
		return nil, err
	}
	if operationID == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	r, err := s.repo.ReadUntrackedOrganization(ctx, &a, scopeID, fileID, operationID)
	if err != nil || ctx.Err() != nil {
		return nil, untrackedOrganizationError(ctx, err)
	}
	if r == nil || r.ExecutionAllowed || r.Operation.ID != operationID || !untrackedOrganizationOperationValid(r.Operation, owner, scopeID, fileID) {
		return nil, ports.ErrUnavailable
	}
	switch r.State {
	case "queued", "running", "completed", "review_required", "failed":
	default:
		return nil, ports.ErrUnavailable
	}
	if r.CompletedFileID != nil && *r.CompletedFileID == uuid.Nil {
		return nil, ports.ErrUnavailable
	}
	if r.RecordedAt != nil && (!untrackedReviewTime(*r.RecordedAt) || r.RecordedAt.Before(r.Operation.CreatedAt)) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	if r.CompletedFileID != nil {
		id := *r.CompletedFileID
		out.CompletedFileID = &id
	}
	if r.RecordedAt != nil {
		at := *r.RecordedAt
		out.RecordedAt = &at
	}
	return &out, nil
}
