package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func (s *ServerHandler) SetFeedService(f *usecaseSearch.FeedService) { s.feedSvc = f }
func rssDetails(r *usecaseSearch.FeedInfo) IndexerRSSDetails {
	return IndexerRSSDetails{IndexerId: r.IndexerID, Enabled: r.Enabled, Kind: IndexerRSSDetailsKind(r.Kind), IntervalSeconds: int(r.IntervalSeconds), ConfigVersion: r.ConfigVersion, NextPollAt: r.NextPollAt, LastQueryId: r.LastQueryID, LastSuccessAt: r.LastSuccessAt, LastCandidateCount: r.LastCandidateCount}
}
func (s *ServerHandler) GetIndexerRSS(ctx context.Context, r GetIndexerRSSRequestObject) (GetIndexerRSSResponseObject, error) {
	a := GetActor(ctx)
	id := GetRequestID(ctx)
	if a == nil || !a.IsAdmin() {
		return GetIndexerRSS403JSONResponse{Code: "FORBIDDEN", Message: "administrator access required", RequestId: id}, nil
	}
	if s.feedSvc == nil {
		return GetIndexerRSS503JSONResponse{Code: "UNAVAILABLE", Message: "RSS service unavailable", RequestId: id}, nil
	}
	info, e := s.feedSvc.Get(ctx, a, r.Id)
	if errors.Is(e, ports.ErrNotFound) {
		return GetIndexerRSS404JSONResponse{Code: "NOT_FOUND", Message: "indexer not found", RequestId: id}, nil
	}
	if e != nil {
		return GetIndexerRSS503JSONResponse{Code: "UNAVAILABLE", Message: "RSS service unavailable", RequestId: id}, nil
	}
	return GetIndexerRSS200JSONResponse(rssDetails(info)), nil
}
func (s *ServerHandler) ConfigureIndexerRSS(ctx context.Context, r ConfigureIndexerRSSRequestObject) (ConfigureIndexerRSSResponseObject, error) {
	a := GetActor(ctx)
	id := GetRequestID(ctx)
	if a == nil || !a.IsAdmin() {
		return ConfigureIndexerRSS403JSONResponse{Code: "FORBIDDEN", Message: "administrator access required", RequestId: id}, nil
	}
	if s.feedSvc == nil {
		return ConfigureIndexerRSS503JSONResponse{Code: "UNAVAILABLE", Message: "RSS service unavailable", RequestId: id}, nil
	}
	if r.Body == nil || r.Body.IntervalSeconds < 30 || r.Body.IntervalSeconds > 86400 {
		return ConfigureIndexerRSS400JSONResponse{Code: "INVALID_CONFIG", Message: "invalid RSS configuration", RequestId: id}, nil
	}
	c := usecaseSearch.FeedCommand{Enabled: r.Body.Enabled, Kind: string(r.Body.Kind), IntervalSeconds: int32(r.Body.IntervalSeconds), ExpectedVersion: r.Body.ExpectedVersion}
	if r.Body.FeedUrl != nil {
		c.URL = *r.Body.FeedUrl
	}
	info, e := s.feedSvc.Configure(ctx, a, r.Id, c, r.Params.IdempotencyKey)
	if errors.Is(e, usecaseSearch.ErrFeedConfig) {
		return ConfigureIndexerRSS400JSONResponse{Code: "INVALID_CONFIG", Message: "invalid RSS configuration", RequestId: id}, nil
	}
	if errors.Is(e, usecaseSearch.ErrFeedConflict) {
		return ConfigureIndexerRSS409JSONResponse{Code: "VERSION_CONFLICT", Message: "configuration version or idempotency fingerprint changed", RequestId: id}, nil
	}
	if errors.Is(e, ports.ErrNotFound) {
		return ConfigureIndexerRSS404JSONResponse{Code: "NOT_FOUND", Message: "indexer not found", RequestId: id}, nil
	}
	if e != nil {
		return ConfigureIndexerRSS503JSONResponse{Code: "UNAVAILABLE", Message: "RSS service unavailable", RequestId: id}, nil
	}
	return ConfigureIndexerRSS200JSONResponse(rssDetails(info)), nil
}
