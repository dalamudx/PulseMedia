package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *ServerHandler) GetDownloadGlobalRates(ctx context.Context, _ GetDownloadGlobalRatesRequestObject) (GetDownloadGlobalRatesResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetDownloadGlobalRates401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	if actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return GetDownloadGlobalRates403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "administrator session required", id)), nil
	}
	if s.globalRatesSvc == nil {
		return GetDownloadGlobalRates503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "transfer observations unavailable", id)), nil
	}
	page, err := s.globalRatesSvc.Read(ctx, actor)
	if errors.Is(err, ports.ErrForbidden) {
		return GetDownloadGlobalRates403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "administrator session required", id)), nil
	}
	if err != nil || page == nil {
		return GetDownloadGlobalRates503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "transfer observations unavailable", id)), nil
	}
	out := DownloadGlobalRatesOverview{Enabled: page.Enabled, ObservedAt: page.ObservedAt, FreshnessSeconds: DownloadGlobalRatesOverviewFreshnessSeconds(page.FreshnessSeconds), DownloadBytesPerSecond: page.DownloadBytesPerSecond, UploadBytesPerSecond: page.UploadBytesPerSecond, DownloadKnownClients: page.DownloadKnownClients, UploadKnownClients: page.UploadKnownClients, Clients: []DownloadGlobalRateClient{}, Series: []DownloadGlobalRatePoint{}}
	for _, item := range page.Clients {
		client := DownloadGlobalRateClient{Id: item.ID, Name: item.Name, ClientType: DownloadGlobalRateClientClientType(item.ClientType), State: DownloadGlobalRateClientState(item.State), FailureCode: DownloadGlobalRateClientFailureCode(item.FailureCode), ObservedAt: item.ObservedAt, DownloadBytesPerSecond: item.DownloadBytesPerSecond, UploadBytesPerSecond: item.UploadBytesPerSecond}
		if item.LastGood != nil {
			client.LastGood = &DownloadGlobalRateValues{ObservedAt: item.LastGood.ObservedAt, DownloadBytesPerSecond: item.LastGood.DownloadBytesPerSecond, UploadBytesPerSecond: item.LastGood.UploadBytesPerSecond}
		}
		out.Clients = append(out.Clients, client)
	}
	for _, point := range page.Series {
		out.Series = append(out.Series, DownloadGlobalRatePoint{At: point.At, DownloadBytesPerSecond: point.DownloadBytesPerSecond, UploadBytesPerSecond: point.UploadBytesPerSecond, DownloadKnownClients: point.DownloadKnownClients, UploadKnownClients: point.UploadKnownClients})
	}
	return GetDownloadGlobalRates200JSONResponse(out), nil
}
