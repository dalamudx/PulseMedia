package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

func (s *ServerHandler) SetInteractiveSearchService(service *usecaseSearch.InteractiveSearchService) {
	s.interactiveSearchSvc = service
}

// A single explicit projection prevents secret/internal candidate fields from
// reaching JSON and applies the same cache contract to all four endpoints.
type interactiveSearchHTTPResponse struct {
	status int
	body   any
}

func (r interactiveSearchHTTPResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Add("Vary", "Cookie")
	w.Header().Add("Vary", "Authorization")
	w.WriteHeader(r.status)
	return json.NewEncoder(w).Encode(r.body)
}
func (r interactiveSearchHTTPResponse) VisitCreateReleaseSearchResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r interactiveSearchHTTPResponse) VisitGetReleaseSearchResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r interactiveSearchHTTPResponse) VisitCancelReleaseSearchResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r interactiveSearchHTTPResponse) VisitGetReleaseSearchReceiptResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func interactiveSearchFailure(ctx context.Context, err error) interactiveSearchHTTPResponse {
	status, code, message := http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "search service unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden), errors.Is(err, ports.ErrUserDisabled):
		status, code, message = http.StatusForbidden, "FORBIDDEN", "current account cannot access this search"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "search or original command not found"
	case errors.Is(err, ports.ErrInteractiveSearchConflict), errors.Is(err, ports.ErrCommandConflict):
		status, code, message = http.StatusConflict, "SEARCH_CONFLICT", "command changed; reread the original receipt"
	case errors.Is(err, ports.ErrRejected), errors.Is(err, ports.ErrBadResponse):
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "invalid search request"
	}
	return interactiveSearchHTTPResponse{status: status, body: ErrorResponse{Code: code, Message: message, RequestId: GetRequestID(ctx)}}
}

func (s *ServerHandler) CreateReleaseSearch(ctx context.Context, r CreateReleaseSearchRequestObject) (CreateReleaseSearchResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.interactiveSearchSvc == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return interactiveSearchFailure(ctx, ports.ErrRejected), nil
	}
	header, err := s.interactiveSearchSvc.Create(ctx, actor, ports.InteractiveSearchInput{Query: r.Body.Query, MediaID: r.Body.MediaId, SubscriptionID: r.Body.SubscriptionId}, r.Params.IdempotencyKey)
	if err != nil {
		return interactiveSearchFailure(ctx, err), nil
	}
	return interactiveSearchHTTPResponse{status: http.StatusCreated, body: interactiveSearchReceipt(*header)}, nil
}
func (s *ServerHandler) GetReleaseSearch(ctx context.Context, r GetReleaseSearchRequestObject) (GetReleaseSearchResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.interactiveSearchSvc == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnavailable), nil
	}
	snapshot, err := s.interactiveSearchSvc.Read(ctx, actor, r.Id)
	if err != nil {
		return interactiveSearchFailure(ctx, err), nil
	}
	return interactiveSearchHTTPResponse{status: http.StatusOK, body: interactiveSearchProjection(*snapshot)}, nil
}
func (s *ServerHandler) CancelReleaseSearch(ctx context.Context, r CancelReleaseSearchRequestObject) (CancelReleaseSearchResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.interactiveSearchSvc == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnavailable), nil
	}
	header, err := s.interactiveSearchSvc.Cancel(ctx, actor, r.Id, r.Params.IdempotencyKey)
	if err != nil {
		return interactiveSearchFailure(ctx, err), nil
	}
	return interactiveSearchHTTPResponse{status: http.StatusOK, body: interactiveSearchReceipt(*header)}, nil
}
func (s *ServerHandler) GetReleaseSearchReceipt(ctx context.Context, r GetReleaseSearchReceiptRequestObject) (GetReleaseSearchReceiptResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.interactiveSearchSvc == nil {
		return interactiveSearchFailure(ctx, ports.ErrUnavailable), nil
	}
	header, err := s.interactiveSearchSvc.Receipt(ctx, actor, r.Key, string(r.Params.Action))
	if err != nil {
		return interactiveSearchFailure(ctx, err), nil
	}
	return interactiveSearchHTTPResponse{status: http.StatusOK, body: interactiveSearchReceipt(*header)}, nil
}

func interactiveSearchReceipt(h ports.InteractiveSearchHeader) InteractiveReleaseSearchReceipt {
	return InteractiveReleaseSearchReceipt{Id: h.ID, Query: h.Query, MediaId: h.MediaID, SubscriptionId: h.SubscriptionID, CreatedAt: h.CreatedAt, Deadline: h.Deadline, CancelledAt: h.CancelledAt, SourceCount: h.SourceCount, RankingPolicyId: h.RankingPolicy.ID, RankingPolicyRevision: h.RankingPolicy.Revision}
}
func interactiveSearchProjection(s ports.InteractiveSearchSnapshot) InteractiveReleaseSearchSnapshot {
	out := InteractiveReleaseSearchSnapshot{Search: interactiveSearchReceipt(s.Header), State: s.State, Partial: s.Partial, Truncated: s.Truncated, TotalCandidates: s.TotalCandidates, StatisticsMode: "latest_persisted", SortOrder: "quality_desc_seeders_desc_id_asc", Results: make([]ReleaseItemResult, 0, len(s.Candidates)), Sources: make([]InteractiveReleaseSearchSource, 0, len(s.Sources))}
	if len(s.Header.RankingPolicy.CustomWords) != 0 {
		out.SortOrder = "quality_preference_desc_seeders_desc_id_asc"
	}
	for _, source := range s.Sources {
		ids := append(make([]uuid.UUID, 0, len(source.QueryIDs)), source.QueryIDs...)
		out.Sources = append(out.Sources, InteractiveReleaseSearchSource{IndexerId: source.IndexerID, Name: source.Name, ConfigVersion: source.ConfigVersion, State: source.State, Reason: source.Reason, QueryIds: ids, Partial: source.Partial})
	}
	for _, row := range s.Candidates {
		c := row.Candidate
		item := ReleaseItemResult{Id: c.ID, Title: c.Title, SizeBytes: c.SizeBytes, Seeders: int(c.Seeders), Leechers: int(c.Leechers), IndexerName: row.SourceName, Score: &row.QualityScore}
		if row.PreferenceScore != 0 {
			item.PreferenceScore = &row.PreferenceScore
		}
		var profile quality.Profile
		if len(c.QualityProfile) <= 16384 && json.Unmarshal(c.QualityProfile, &profile) == nil && quality.KnownProfile(profile) {
			resolution, source, codec := string(profile.Resolution), string(profile.Source), string(profile.Codec)
			item.Resolution = &resolution
			item.Source = &source
			item.Codec = &codec
		}
		out.Results = append(out.Results, item)
	}
	return out
}
