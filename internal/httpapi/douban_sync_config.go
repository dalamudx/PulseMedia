package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
)

func (s *ServerHandler) SetDoubanSyncConfigService(service *watchlist.ConfigService) {
	s.doubanSyncConfigSvc = service
}

func doubanSyncConfigActorError(actor *ports.Actor) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || id.String() != actor.ID || actor.Type != ports.ActorTypeUser || actor.Scopes != nil || (actor.Role != ports.RoleMember && actor.Role != ports.RoleAdmin) {
		return ports.ErrForbidden
	}
	return nil
}

func (s *ServerHandler) ReadDoubanSyncConfig(ctx context.Context, _ ReadDoubanSyncConfigRequestObject) (ReadDoubanSyncConfigResponseObject, error) {
	actor := GetActor(ctx)
	if err := doubanSyncConfigActorError(actor); err != nil {
		return doubanSyncConfigProblem(ctx, err), nil
	}
	config, err := s.doubanSyncConfigSvc.Read(ctx, actor)
	if err != nil {
		return doubanSyncConfigProblem(ctx, err), nil
	}
	var out *DoubanSyncConfig
	if config != nil {
		value := doubanSyncConfigResponse(*config)
		out = &value
	}
	return ReadDoubanSyncConfig200JSONResponse{Config: out}, nil
}

func (s *ServerHandler) SaveDoubanSyncConfig(ctx context.Context, request SaveDoubanSyncConfigRequestObject) (SaveDoubanSyncConfigResponseObject, error) {
	actor := GetActor(ctx)
	if err := doubanSyncConfigActorError(actor); err != nil {
		return doubanSyncConfigProblem(ctx, err), nil
	}
	if request.Body == nil || !validDoubanSyncCommandKey(request.Params.IdempotencyKey) {
		return doubanSyncConfigProblem(ctx, ports.ErrRejected), nil
	}
	b := request.Body
	receipt, err := s.doubanSyncConfigSvc.Save(ctx, actor, ports.DoubanSyncInput{DoubanUID: b.DoubanUid, Enabled: b.Enabled, ExpectedVersion: b.ExpectedVersion, MovieRootID: b.MovieRootId, TVRootID: b.TvRootId}, request.Params.IdempotencyKey)
	if err != nil {
		return doubanSyncConfigProblem(ctx, err), nil
	}
	return SaveDoubanSyncConfig200JSONResponse{CommandKey: receipt.CommandKey, Config: doubanSyncConfigResponse(receipt.Config)}, nil
}

func (s *ServerHandler) ReadDoubanSyncConfigReceipt(ctx context.Context, request ReadDoubanSyncConfigReceiptRequestObject) (ReadDoubanSyncConfigReceiptResponseObject, error) {
	actor := GetActor(ctx)
	if err := doubanSyncConfigActorError(actor); err != nil {
		return doubanSyncConfigProblem(ctx, err), nil
	}
	receipt, err := s.doubanSyncConfigSvc.Receipt(ctx, actor, request.CommandKey)
	if err != nil {
		return doubanSyncConfigProblem(ctx, err), nil
	}
	return ReadDoubanSyncConfigReceipt200JSONResponse{CommandKey: receipt.CommandKey, Config: doubanSyncConfigResponse(receipt.Config)}, nil
}

func doubanSyncConfigResponse(c ports.DoubanSyncConfig) DoubanSyncConfig {
	return DoubanSyncConfig{Id: c.ID, OwnerId: c.OwnerID, DoubanUid: c.DoubanUID, Enabled: c.Enabled, MovieRootId: c.MovieRootID, TvRootId: c.TVRootID, Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

type doubanSyncConfigErrorResponse struct{ problem ErrorResponse }

func doubanSyncConfigProblem(ctx context.Context, err error) doubanSyncConfigErrorResponse {
	status, code, message := 503, "DOUBAN_CONFIG_UNAVAILABLE", "Douban configuration is temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "Douban configuration access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = 400, "INVALID_REQUEST", "invalid Douban configuration request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "configuration root or receipt not found"
	case errors.Is(err, ports.ErrSubscriptionBindingsConflict):
		status, code, message = 409, "CONFLICT", "configuration version or command conflicts"
	}
	return doubanSyncConfigErrorResponse{problemResponse(status, code, message, GetRequestID(ctx))}
}
func (r doubanSyncConfigErrorResponse) VisitReadDoubanSyncConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r doubanSyncConfigErrorResponse) VisitSaveDoubanSyncConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r doubanSyncConfigErrorResponse) VisitReadDoubanSyncConfigReceiptResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
