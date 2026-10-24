package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedScopeApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID) (*ports.UntrackedScope, error)
	Save(context.Context, *ports.Actor, uuid.UUID, ports.UntrackedScopeInput, string) (*ports.UntrackedScopeReceipt, error)
	Receipt(context.Context, *ports.Actor, uuid.UUID, string) (*ports.UntrackedScopeReceipt, error)
}

func (s *ServerHandler) SetUntrackedScopeService(service untrackedScopeApplication) {
	s.untrackedScopeSvc = service
}

func untrackedScopeActorError(actor *ports.Actor) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || id.String() != actor.ID || actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return ports.ErrForbidden
	}
	return nil
}

func validUntrackedScopeInput(in ports.UntrackedScopeInput) bool {
	return in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 && in.ClientID != uuid.Nil && !in.ExpectedClientVersion.IsZero() &&
		in.TargetRootID != uuid.Nil && in.ExpectedRootGeneration >= 1 &&
		strings.HasPrefix(in.RemotePrefix, "/") && path.Clean(in.RemotePrefix) == in.RemotePrefix &&
		!strings.ContainsAny(in.RemotePrefix, "\\\x00\r\n")
}

func (s *ServerHandler) ReadUntrackedScope(ctx context.Context, r ReadUntrackedScopeRequestObject) (ReadUntrackedScopeResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil {
		return untrackedScopeProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedScopeSvc == nil {
		return untrackedScopeProblem(ctx, ports.ErrUnavailable), nil
	}
	config, err := s.untrackedScopeSvc.Read(ctx, actor, r.ScopeId)
	if err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	var out *UntrackedScope
	if config != nil {
		value := untrackedScopeResponse(*config)
		out = &value
	}
	return ReadUntrackedScope200JSONResponse{Config: out}, nil
}

func (s *ServerHandler) SaveUntrackedScope(ctx context.Context, r SaveUntrackedScopeRequestObject) (SaveUntrackedScopeResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || r.Body == nil || !validDoubanSyncCommandKey(r.Params.IdempotencyKey) {
		return untrackedScopeProblem(ctx, ports.ErrRejected), nil
	}
	b := r.Body
	in := ports.UntrackedScopeInput{
		Enabled: b.Enabled, ExpectedVersion: b.ExpectedVersion, ClientID: b.ClientId,
		ExpectedClientVersion: b.ExpectedClientVersion, RemotePrefix: b.RemotePrefix,
		TargetRootID: b.TargetRootId, ExpectedRootGeneration: b.ExpectedRootGeneration,
	}
	if !validUntrackedScopeInput(in) {
		return untrackedScopeProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedScopeSvc == nil {
		return untrackedScopeProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.untrackedScopeSvc.Save(ctx, actor, r.ScopeId, in, r.Params.IdempotencyKey)
	if err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	if receipt == nil {
		return untrackedScopeProblem(ctx, ports.ErrUnavailable), nil
	}
	return SaveUntrackedScope200JSONResponse{CommandKey: receipt.CommandKey, Scope: untrackedScopeResponse(receipt.Scope), ExecutionAllowed: false}, nil
}

func (s *ServerHandler) ReadUntrackedScopeReceipt(ctx context.Context, r ReadUntrackedScopeReceiptRequestObject) (ReadUntrackedScopeReceiptResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil || !validDoubanSyncCommandKey(r.CommandKey) {
		return untrackedScopeProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedScopeSvc == nil {
		return untrackedScopeProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.untrackedScopeSvc.Receipt(ctx, actor, r.ScopeId, r.CommandKey)
	if err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	if receipt == nil {
		return untrackedScopeProblem(ctx, ports.ErrUnavailable), nil
	}
	return ReadUntrackedScopeReceipt200JSONResponse{CommandKey: receipt.CommandKey, Scope: untrackedScopeResponse(receipt.Scope), ExecutionAllowed: false}, nil
}

func untrackedScopeResponse(c ports.UntrackedScope) UntrackedScope {
	return UntrackedScope{
		Id: c.ID, OwnerId: c.OwnerID, Enabled: c.Enabled, Version: c.Version,
		ClientId: c.ClientID, ClientVersion: c.ClientVersion, ClientStamp: c.ClientStamp,
		RemotePrefix: c.RemotePrefix, LocalRoot: c.LocalRoot,
		SourceIdentity: UntrackedScopeIdentity{Device: c.SourceIdentity.Device, Inode: c.SourceIdentity.Inode},
		TargetRootId:   c.TargetRootID, TargetRootPath: c.TargetRootPath,
		TargetRootGeneration: c.TargetRootGeneration, TargetMediaType: c.TargetMediaType,
		TargetMarker: c.TargetMarker, TargetBaselineScanId: c.TargetBaselineScanID,
		TargetIdentity: UntrackedScopeIdentity{Device: c.TargetIdentity.Device, Inode: c.TargetIdentity.Inode},
		CreatedAt:      c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type untrackedScopeErrorResponse struct{ problem ErrorResponse }

func untrackedScopeProblem(ctx context.Context, err error) untrackedScopeErrorResponse {
	status, code, message := 503, "UNTRACKED_SCOPE_UNAVAILABLE", "untracked scope configuration is temporarily unavailable"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Runtime failures never disclose their cause, even when wrapped.
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "untracked scope access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = 400, "INVALID_REQUEST", "invalid untracked scope request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "scope or command receipt not found"
	case errors.Is(err, ports.ErrEndpointVersionConflict):
		status, code, message = 409, "CONFLICT", "configuration version or command conflicts"
	}
	return untrackedScopeErrorResponse{problemResponse(status, code, message, GetRequestID(ctx))}
}

func (r untrackedScopeErrorResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r untrackedScopeErrorResponse) VisitReadUntrackedScopeResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitSaveUntrackedScopeResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r untrackedScopeErrorResponse) VisitReadUntrackedScopeReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}
