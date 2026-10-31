package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
)

type untrackedCompanionApplication interface {
	Discover(context.Context, *ports.Actor, uuid.UUID) ([]ports.CompanionCandidate, error)
	Select(context.Context, *ports.Actor, uuid.UUID, string, ports.UntrackedCompanionSelection) (*ports.CompanionView, error)
	Read(context.Context, *ports.Actor, uuid.UUID) (*ports.CompanionView, error)
	Execute(context.Context, *ports.Actor, uuid.UUID, uuid.UUID) (*ports.CompanionAttachment, error)
}

func (s *ServerHandler) SetUntrackedCompanionsService(service untrackedCompanionApplication) {
	s.untrackedCompanionsSvc = service
}
func untrackedCompanionProblem(ctx context.Context, err error) untrackedScopeErrorResponse {
	if errors.Is(err, ports.ErrTransferReviewRequired) || errors.Is(err, ports.ErrTransferExecutionRejected) {
		err = ports.ErrEndpointVersionConflict
	}
	return untrackedOrganizationProblem(ctx, err)
}
func (s *ServerHandler) companionActor(ctx context.Context, op uuid.UUID) (*ports.Actor, error) {
	a := GetActor(ctx)
	if err := untrackedScopeActorError(a); err != nil {
		return nil, err
	}
	if op == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s.untrackedCompanionsSvc == nil {
		return nil, ports.ErrUnavailable
	}
	return a, nil
}
func untrackedCompanionCandidateResponse(c ports.CompanionCandidate) UntrackedCompanionCandidate {
	return UntrackedCompanionCandidate{Key: c.Key, SourceRelativePath: c.SourceRelativePath, TargetRelativePath: c.TargetRelativePath}
}
func untrackedCompanionAttachmentResponse(a ports.CompanionAttachment) UntrackedCompanionAttachment {
	return UntrackedCompanionAttachment{Id: a.ID, OperationId: a.OperationID, Candidate: untrackedCompanionCandidateResponse(a.Candidate), Action: UntrackedCompanionAttachmentAction(a.Action), State: UntrackedCompanionAttachmentState(a.State), SourceRetained: UntrackedCompanionAttachmentSourceRetained(a.SourceRetained)}
}
func untrackedCompanionViewResponse(v *ports.CompanionView) UntrackedCompanionView {
	out := UntrackedCompanionView{VideoState: UntrackedCompanionViewVideoState(v.VideoState), Items: []UntrackedCompanionAttachment{}}
	for _, a := range v.Items {
		out.Items = append(out.Items, untrackedCompanionAttachmentResponse(a))
	}
	return out
}
func (s *ServerHandler) DiscoverUntrackedCompanions(ctx context.Context, r DiscoverUntrackedCompanionsRequestObject) (DiscoverUntrackedCompanionsResponseObject, error) {
	a, err := s.companionActor(ctx, r.OperationId)
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	items, err := s.untrackedCompanionsSvc.Discover(ctx, a, r.OperationId)
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	out := UntrackedCompanionCandidates{Items: []UntrackedCompanionCandidate{}}
	for _, item := range items {
		out.Items = append(out.Items, untrackedCompanionCandidateResponse(item))
	}
	cache := "private, no-store"
	return DiscoverUntrackedCompanions200JSONResponse{Body: out, Headers: DiscoverUntrackedCompanions200ResponseHeaders{CacheControl: &cache}}, nil
}
func (s *ServerHandler) ReadUntrackedCompanions(ctx context.Context, r ReadUntrackedCompanionsRequestObject) (ReadUntrackedCompanionsResponseObject, error) {
	a, err := s.companionActor(ctx, r.OperationId)
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	view, err := s.untrackedCompanionsSvc.Read(ctx, a, r.OperationId)
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	cache := "private, no-store"
	return ReadUntrackedCompanions200JSONResponse{Body: untrackedCompanionViewResponse(view), Headers: ReadUntrackedCompanions200ResponseHeaders{CacheControl: &cache}}, nil
}
func (s *ServerHandler) SelectUntrackedCompanions(ctx context.Context, r SelectUntrackedCompanionsRequestObject) (SelectUntrackedCompanionsResponseObject, error) {
	a, err := s.companionActor(ctx, r.OperationId)
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	if r.Body == nil || !validDoubanSyncCommandKey(r.Params.IdempotencyKey) {
		return untrackedCompanionProblem(ctx, ports.ErrRejected), nil
	}
	view, err := s.untrackedCompanionsSvc.Select(ctx, a, r.OperationId, r.Params.IdempotencyKey, ports.UntrackedCompanionSelection{CandidateKeys: r.Body.CandidateKeys, Action: string(r.Body.Action)})
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	cache := "private, no-store"
	return SelectUntrackedCompanions200JSONResponse{Body: untrackedCompanionViewResponse(view), Headers: SelectUntrackedCompanions200ResponseHeaders{CacheControl: &cache}}, nil
}
func (s *ServerHandler) ExecuteUntrackedCompanion(ctx context.Context, r ExecuteUntrackedCompanionRequestObject) (ExecuteUntrackedCompanionResponseObject, error) {
	a, err := s.companionActor(ctx, r.OperationId)
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	if r.AttachmentId == uuid.Nil {
		return untrackedCompanionProblem(ctx, ports.ErrRejected), nil
	}
	result, err := s.untrackedCompanionsSvc.Execute(ctx, a, r.OperationId, r.AttachmentId)
	if err != nil {
		return untrackedCompanionProblem(ctx, err), nil
	}
	cache := "private, no-store"
	return ExecuteUntrackedCompanion200JSONResponse{Body: untrackedCompanionAttachmentResponse(*result), Headers: ExecuteUntrackedCompanion200ResponseHeaders{CacheControl: &cache}}, nil
}
func (r untrackedScopeErrorResponse) VisitDiscoverUntrackedCompanionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitReadUntrackedCompanionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitSelectUntrackedCompanionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitExecuteUntrackedCompanionResponse(w http.ResponseWriter) error {
	return r.write(w)
}
