package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func (s *ServerHandler) SetTVCoverageCandidateReviewService(service *subscription.TVCoverageCandidateReviewService) {
	s.tvCoverageReviewSvc = service
}

type tvCoverageReviewResponse struct{ episodeSourceResponse }

func (r tvCoverageReviewResponse) VisitPreviewTVCoverageCandidateReviewResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r tvCoverageReviewResponse) VisitReviewTVCoverageCandidateResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func tvCoverageReviewFailure(ctx context.Context, e error) tvCoverageReviewResponse {
	if errors.Is(e, ports.ErrInvalidTVCoverageReview) {
		e = ports.ErrInvalidEpisodeMapping
	}
	if errors.Is(e, ports.ErrTVCoverageReviewConflict) {
		e = ports.ErrEpisodeSourceConflict
	}
	return tvCoverageReviewResponse{episodeSourceFailure(ctx, e)}
}
func (s *ServerHandler) PreviewTVCoverageCandidateReview(ctx context.Context, r PreviewTVCoverageCandidateReviewRequestObject) (PreviewTVCoverageCandidateReviewResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return tvCoverageReviewFailure(ctx, ports.ErrUnauthorized), nil
	}
	if a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return tvCoverageReviewFailure(ctx, ports.ErrForbidden), nil
	}
	if s.tvCoverageReviewSvc == nil {
		return tvCoverageReviewFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return tvCoverageReviewFailure(ctx, ports.ErrInvalidTVCoverageReview), nil
	}
	out, e := s.tvCoverageReviewSvc.Preview(ctx, a, r.Id, r.Body.CandidateId)
	if e != nil {
		return tvCoverageReviewFailure(ctx, e), nil
	}
	return tvCoverageReviewResponse{episodeSourceResponse{body: out}}, nil
}
func (s *ServerHandler) ReviewTVCoverageCandidate(ctx context.Context, r ReviewTVCoverageCandidateRequestObject) (ReviewTVCoverageCandidateResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return tvCoverageReviewFailure(ctx, ports.ErrUnauthorized), nil
	}
	if a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return tvCoverageReviewFailure(ctx, ports.ErrForbidden), nil
	}
	if s.tvCoverageReviewSvc == nil {
		return tvCoverageReviewFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return tvCoverageReviewFailure(ctx, ports.ErrInvalidTVCoverageReview), nil
	}
	c := ports.TVCoverageCandidateReviewCommand{SubscriptionID: r.Id, CandidateID: r.Body.CandidateId, ExpectedInputStamp: r.Body.ExpectedInputStamp, Confirmed: bool(r.Body.Confirmed), Reason: r.Body.Reason}
	out, e := s.tvCoverageReviewSvc.Review(ctx, a, c, r.Params.IdempotencyKey)
	if e != nil {
		return tvCoverageReviewFailure(ctx, e), nil
	}
	return tvCoverageReviewResponse{episodeSourceResponse{body: out}}, nil
}

// Reject extra authority fields and trailing bodies before generated decoding discards them.
func strictTVCoverageReviewJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preview := strings.HasSuffix(r.URL.Path, "/tv-coverage-candidate-reviews/preview")
		review := strings.HasSuffix(r.URL.Path, "/tv-coverage-candidate-reviews")
		if r.Method != http.MethodPost || (!preview && !review) {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context()))
		}
		if r.Body == nil || (review && len(r.Header.Values("Idempotency-Key")) != 1) {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		var body any = &ReviewTVCoverageCandidateJSONRequestBody{}
		if preview {
			body = &PreviewTVCoverageCandidateReviewJSONRequestBody{}
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(body) != nil || decoder.Decode(new(any)) != io.EOF {
			fail()
			return
		}
		raw, e := json.Marshal(body)
		if e != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
