package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedOrganizationApplication interface {
	Organize(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, ports.UntrackedOrganizationInput, string) (*ports.UntrackedOrganizationReceipt, error)
	Receipt(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.UntrackedOrganizationReceipt, error)
	Read(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, uuid.UUID) (*ports.UntrackedOrganizationView, error)
}

func (s *ServerHandler) SetUntrackedOrganizationService(service untrackedOrganizationApplication) {
	s.untrackedOrganizationSvc = service
}

func untrackedOrganizationProblem(ctx context.Context, err error) untrackedScopeErrorResponse {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.Is(err, ports.ErrUntrackedOrganizationStale) {
		err = ports.ErrEndpointVersionConflict
	}
	return untrackedScopeProblem(ctx, err)
}

func (s *ServerHandler) RequestUntrackedOrganization(ctx context.Context, r RequestUntrackedOrganizationRequestObject) (RequestUntrackedOrganizationResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedOrganizationProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || r.Body == nil || r.Body.ConfirmationId == uuid.Nil || !validDoubanSyncCommandKey(r.Params.IdempotencyKey) {
		return untrackedOrganizationProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedOrganizationSvc == nil {
		return untrackedOrganizationProblem(ctx, ports.ErrUnavailable), nil
	}
	input := ports.UntrackedOrganizationInput{ConfirmationID: r.Body.ConfirmationId}
	receipt, err := s.untrackedOrganizationSvc.Organize(ctx, actor, r.ScopeId, r.FileId, input, r.Params.IdempotencyKey)
	if err != nil {
		return untrackedOrganizationProblem(ctx, err), nil
	}
	if !validUntrackedOrganizationReceipt(receipt, actor.ID, r.ScopeId, r.FileId, r.Params.IdempotencyKey) || receipt.Operation.ConfirmationID != input.ConfirmationID {
		return untrackedOrganizationProblem(ctx, ports.ErrUnavailable), nil
	}
	cache := "private, no-store"
	return RequestUntrackedOrganization202JSONResponse{
		Body:    untrackedOrganizationReceiptResponse(*receipt),
		Headers: RequestUntrackedOrganization202ResponseHeaders{CacheControl: &cache},
	}, nil
}

func (s *ServerHandler) ReadUntrackedOrganizationReceipt(ctx context.Context, r ReadUntrackedOrganizationReceiptRequestObject) (ReadUntrackedOrganizationReceiptResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedOrganizationProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || !validDoubanSyncCommandKey(r.CommandKey) {
		return untrackedOrganizationProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedOrganizationSvc == nil {
		return untrackedOrganizationProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.untrackedOrganizationSvc.Receipt(ctx, actor, r.ScopeId, r.FileId, r.CommandKey)
	if err != nil {
		return untrackedOrganizationProblem(ctx, err), nil
	}
	if !validUntrackedOrganizationReceipt(receipt, actor.ID, r.ScopeId, r.FileId, r.CommandKey) {
		return untrackedOrganizationProblem(ctx, ports.ErrUnavailable), nil
	}
	cache := "private, no-store"
	return ReadUntrackedOrganizationReceipt200JSONResponse{
		Body:    untrackedOrganizationReceiptResponse(*receipt),
		Headers: ReadUntrackedOrganizationReceipt200ResponseHeaders{CacheControl: &cache},
	}, nil
}

func (s *ServerHandler) ReadUntrackedOrganization(ctx context.Context, r ReadUntrackedOrganizationRequestObject) (ReadUntrackedOrganizationResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedOrganizationProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.FileId == uuid.Nil || r.OperationId == uuid.Nil {
		return untrackedOrganizationProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedOrganizationSvc == nil {
		return untrackedOrganizationProblem(ctx, ports.ErrUnavailable), nil
	}
	view, err := s.untrackedOrganizationSvc.Read(ctx, actor, r.ScopeId, r.FileId, r.OperationId)
	if err != nil {
		return untrackedOrganizationProblem(ctx, err), nil
	}
	if !validUntrackedOrganizationView(view, actor.ID, r.ScopeId, r.FileId, r.OperationId) {
		return untrackedOrganizationProblem(ctx, ports.ErrUnavailable), nil
	}
	cache := "private, no-store"
	return ReadUntrackedOrganization200JSONResponse{
		Body:    untrackedOrganizationViewResponse(*view),
		Headers: ReadUntrackedOrganization200ResponseHeaders{CacheControl: &cache},
	}, nil
}

func validUntrackedOrganizationOperation(o ports.UntrackedOrganizationOperation, actor string, scopeID, fileID uuid.UUID) bool {
	return o.ID != uuid.Nil && o.ActorID.String() == actor && o.ScopeID == scopeID && o.FileID == fileID &&
		o.ConfirmationID != uuid.Nil && o.PlanID != uuid.Nil && o.StepID != uuid.Nil && o.JobID != uuid.Nil &&
		validUntrackedReviewVersion(o.ScopeVersion) && validUntrackedReviewVersion(o.FileRevision) &&
		validUntrackedReviewHash(o.MaterialHash) && validUntrackedReviewTime(o.CreatedAt)
}

func validUntrackedOrganizationReceipt(r *ports.UntrackedOrganizationReceipt, actor string, scopeID, fileID uuid.UUID, key string) bool {
	return r != nil && !r.ExecutionAllowed && r.CommandKey == key && r.State == "queued" &&
		validUntrackedOrganizationOperation(r.Operation, actor, scopeID, fileID)
}

func validUntrackedOrganizationView(v *ports.UntrackedOrganizationView, actor string, scopeID, fileID, operationID uuid.UUID) bool {
	if v == nil || v.ExecutionAllowed || v.Operation.ID != operationID || !validUntrackedOrganizationOperation(v.Operation, actor, scopeID, fileID) {
		return false
	}
	if v.RecordedAt != nil && (!validUntrackedReviewTime(*v.RecordedAt) || v.RecordedAt.Before(v.Operation.CreatedAt)) {
		return false
	}
	if v.ReviewRequired != (v.State == "review_required") {
		return false
	}
	switch v.State {
	case "completed":
		return v.CompletedFileID != nil && *v.CompletedFileID != uuid.Nil && v.RecordedAt != nil
	case "queued", "running", "review_required", "failed":
		return v.CompletedFileID == nil
	default:
		return false
	}
}

func untrackedOrganizationOperationResponse(o ports.UntrackedOrganizationOperation) UntrackedOrganizationOperation {
	return UntrackedOrganizationOperation{
		Id: o.ID, ActorId: o.ActorID, ScopeId: o.ScopeID, FileId: o.FileID,
		ConfirmationId: o.ConfirmationID, PlanId: o.PlanID, StepId: o.StepID, JobId: o.JobID,
		ScopeVersion: o.ScopeVersion, FileRevision: o.FileRevision, MaterialHash: o.MaterialHash, CreatedAt: o.CreatedAt,
	}
}

func untrackedOrganizationReceiptResponse(r ports.UntrackedOrganizationReceipt) UntrackedOrganizationReceipt {
	return UntrackedOrganizationReceipt{
		CommandKey: r.CommandKey, Operation: untrackedOrganizationOperationResponse(r.Operation), State: r.State, ExecutionAllowed: false,
	}
}

func untrackedOrganizationViewResponse(v ports.UntrackedOrganizationView) UntrackedOrganizationView {
	return UntrackedOrganizationView{
		Operation: untrackedOrganizationOperationResponse(v.Operation), State: v.State, ReviewRequired: v.ReviewRequired,
		CompletedFileId: v.CompletedFileID, RecordedAt: v.RecordedAt, ExecutionAllowed: false,
	}
}

func (r untrackedScopeErrorResponse) VisitRequestUntrackedOrganizationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r untrackedScopeErrorResponse) VisitReadUntrackedOrganizationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r untrackedScopeErrorResponse) VisitReadUntrackedOrganizationReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}
