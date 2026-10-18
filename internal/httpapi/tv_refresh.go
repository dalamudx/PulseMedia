package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

func (s *ServerHandler) SetTVRefreshService(service *subscription.TVRefreshService) {
	s.tvRefreshSvc = service
}
func (s *ServerHandler) RefreshTVEpisodeMetadata(ctx context.Context, r RefreshTVEpisodeMetadataRequestObject) (RefreshTVEpisodeMetadataResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return RefreshTVEpisodeMetadata401JSONResponse{PolicyUnauthorizedJSONResponse: PolicyUnauthorizedJSONResponse{Code: "UNAUTHORIZED", Message: "authentication required", RequestId: GetRequestID(ctx)}}, nil
	}
	if !a.IsAdmin() {
		return RefreshTVEpisodeMetadata403JSONResponse{PolicyForbiddenJSONResponse: PolicyForbiddenJSONResponse{Code: "FORBIDDEN", Message: "global metadata refresh requires administrator", RequestId: GetRequestID(ctx)}}, nil
	}
	if s.tvRefreshSvc == nil {
		return tvRefreshFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return tvRefreshFailure(ctx, ports.ErrInvalidSubscriptionBindings), nil
	}
	result, e := s.tvRefreshSvc.Refresh(ctx, a, r.Id, ports.TVRefreshCommand{ExpectedVersion: r.Body.ExpectedVersion, Seasons: r.Body.Seasons}, r.Params.IdempotencyKey)
	if e != nil {
		return tvRefreshFailure(ctx, e), nil
	}
	return tvRefreshSuccess{RefreshTVEpisodeMetadata200JSONResponse{SubscriptionId: result.SubscriptionID, MediaId: result.MediaID, MediaVersion: result.MediaVersion, ObservedEpisodes: result.ObservedEpisodes}}, nil
}

type tvRefreshFailureResponse struct{ controlFailure }

func (e tvRefreshFailureResponse) VisitRefreshTVEpisodeMetadataResponse(w http.ResponseWriter) error {
	return e.VisitControlSubscriptionResponse(w)
}
