package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"net/http"
)

func (s *ServerHandler) SetCandidateExplanationService(service *usecaseSearch.CandidateExplanationService) {
	s.candidateSvc = service
}

type candidateHTTPFailure struct {
	status int
	body   ErrorResponse
}

func (e candidateHTTPFailure) VisitExplainWantedCandidatesResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(e.status)
	return json.NewEncoder(w).Encode(e.body)
}
func candidateFailure(ctx context.Context, e error) candidateHTTPFailure {
	status, code := 503, "UNAVAILABLE"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		status, code = 401, "UNAUTHORIZED"
	case errors.Is(e, ports.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(e, ports.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	}
	return candidateHTTPFailure{status, ErrorResponse{Code: code, Message: "candidate explanation could not be read", RequestId: GetRequestID(ctx)}}
}
func (s *ServerHandler) ExplainWantedCandidates(ctx context.Context, r ExplainWantedCandidatesRequestObject) (ExplainWantedCandidatesResponseObject, error) {
	if s.candidateSvc == nil {
		return candidateFailure(ctx, ports.ErrUnavailable), nil
	}
	report, e := s.candidateSvc.Read(ctx, GetActor(ctx), r.Id, r.WantedId)
	if e != nil {
		return candidateFailure(ctx, e), nil
	}
	out := ExplainWantedCandidates200JSONResponse{SubscriptionId: report.SubscriptionID, WantedId: report.WantedID, RoundId: report.RoundID, IntentVersion: report.IntentVersion, PolicyRevision: report.PolicyRevision, RoundState: report.RoundState, RoundReason: report.RoundReason, ObservedAt: report.ObservedAt, Items: []CandidateExplanationItem{}, Blockers: report.Blockers, Limited: report.Limited}
	for _, row := range report.Items {
		out.Items = append(out.Items, CandidateExplanationItem{Id: row.ID, IndexerId: row.IndexerID, Title: row.Title, Decision: CandidateExplanationItemDecision(row.Decision), Reason: row.Reason, Score: row.Score, MeetsCutoff: row.MeetsCutoff, Breakdown: row.Breakdown, Available: row.Available, Blocked: row.Blocked, WaitReason: row.WaitReason, InputDigest: row.InputDigest, Cached: row.Cached})
	}
	return candidateSuccess{out}, nil
}

type candidateSuccess struct {
	Body ExplainWantedCandidates200JSONResponse
}

func (s candidateSuccess) VisitExplainWantedCandidatesResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(s.Body)
}
