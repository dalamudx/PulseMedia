package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

func (s *ServerHandler) SetEpisodeCandidateReviewService(service *subscription.EpisodeCandidateReviewService) {
	s.episodeReviewSvc = service
}

type episodeCandidateReviewResponse struct{ episodeSourceResponse }

func (r episodeCandidateReviewResponse) VisitReviewEpisodeCandidateResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) ReviewEpisodeCandidate(ctx context.Context, r ReviewEpisodeCandidateRequestObject) (ReviewEpisodeCandidateResponseObject, error) {
	fail := func(e error) (ReviewEpisodeCandidateResponseObject, error) {
		if errors.Is(e, ports.ErrInvalidEpisodeCandidateReview) {
			e = ports.ErrInvalidEpisodeMapping
		}
		if errors.Is(e, ports.ErrEpisodeCandidateReviewConflict) {
			e = ports.ErrEpisodeSourceConflict
		}
		return episodeCandidateReviewResponse{episodeSourceFailure(ctx, e)}, nil
	}
	if a := GetActor(ctx); a == nil {
		return fail(ports.ErrUnauthorized)
	} else if !a.IsAdmin() {
		return fail(ports.ErrForbidden)
	}
	if s.episodeReviewSvc == nil {
		return fail(ports.ErrUnavailable)
	}
	if r.Body == nil {
		return fail(ports.ErrInvalidEpisodeCandidateReview)
	}
	raw, e := json.Marshal(r.Body)
	if e != nil {
		return fail(ports.ErrInvalidEpisodeCandidateReview)
	}
	var c ports.EpisodeCandidateReviewCommand
	if json.Unmarshal(raw, &c) != nil {
		return fail(ports.ErrInvalidEpisodeCandidateReview)
	}
	c.SubscriptionID = r.Id
	out, e := s.episodeReviewSvc.Review(ctx, GetActor(ctx), c, r.Params.IdempotencyKey)
	if e != nil {
		return fail(e)
	}
	return episodeCandidateReviewResponse{episodeSourceResponse{body: out}}, nil
}
