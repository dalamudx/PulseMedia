package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func (s *ServerHandler) SetSubscriptionBindingService(service *subscription.BindingService) {
	s.bindingSvc = service
}
func (s *ServerHandler) ConfigureSubscriptionBindings(ctx context.Context, r ConfigureSubscriptionBindingsRequestObject) (ConfigureSubscriptionBindingsResponseObject, error) {
	id := GetRequestID(ctx)
	a := GetActor(ctx)
	if a == nil {
		return ConfigureSubscriptionBindings401JSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: id}, nil
	}
	if s.bindingSvc == nil {
		return ConfigureSubscriptionBindings503JSONResponse{Code: "UNAVAILABLE", Message: "binding service unavailable", RequestId: id}, nil
	}
	if r.Body == nil {
		return ConfigureSubscriptionBindings400JSONResponse{Code: "INVALID_CONFIG", Message: "invalid binding configuration", RequestId: id}, nil
	}
	info, e := s.bindingSvc.Configure(ctx, a, r.Id, ports.SubscriptionBindingsCommand{ExpectedVersion: r.Body.ExpectedVersion, IndexerIDs: r.Body.IndexerIds, DownloaderID: r.Body.DownloaderId, QualityPolicyID: r.Body.QualityPolicyId}, r.Params.IdempotencyKey)
	switch {
	case errors.Is(e, ports.ErrInvalidSubscriptionBindings):
		return ConfigureSubscriptionBindings400JSONResponse{Code: "INVALID_CONFIG", Message: "invalid endpoint bindings", RequestId: id}, nil
	case errors.Is(e, ports.ErrForbidden):
		return ConfigureSubscriptionBindings403JSONResponse{Code: "FORBIDDEN", Message: "subscription access denied", RequestId: id}, nil
	case errors.Is(e, ports.ErrNotFound):
		return ConfigureSubscriptionBindings404JSONResponse{Code: "NOT_FOUND", Message: "subscription not found", RequestId: id}, nil
	case errors.Is(e, ports.ErrSubscriptionBindingsConflict):
		return ConfigureSubscriptionBindings409JSONResponse{Code: "VERSION_CONFLICT", Message: "intent version or command fingerprint changed", RequestId: id}, nil
	case e != nil:
		return ConfigureSubscriptionBindings503JSONResponse{Code: "UNAVAILABLE", Message: "binding update unavailable", RequestId: id}, nil
	}
	return ConfigureSubscriptionBindings200JSONResponse{Id: info.ID, State: SubscriptionBindingsDetailsState(info.State), IntentVersion: info.IntentVersion, IndexerIds: info.IndexerIDs, DownloaderId: info.DownloaderID}, nil
}
