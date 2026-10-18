package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"net/http"
)

func (s *ServerHandler) SetEpisodeAcquisitionService(service *acquisition.EpisodeService) {
	s.episodeAcquisitionSvc = service
}

type episodeAcquisitionResponse struct{ episodeSourceResponse }

func (r episodeAcquisitionResponse) VisitAcquireReviewedEpisodeResponse(w http.ResponseWriter) error {
	if r.failure != nil {
		return r.write(w)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	return json.NewEncoder(w).Encode(r.body)
}
func (s *ServerHandler) AcquireReviewedEpisode(ctx context.Context, r AcquireReviewedEpisodeRequestObject) (AcquireReviewedEpisodeResponseObject, error) {
	fail := func(e error) (AcquireReviewedEpisodeResponseObject, error) {
		if errors.Is(e, ports.ErrEpisodeAcquisitionConflict) || errors.Is(e, ports.ErrAcquisitionUnitAlreadyReserved) {
			e = ports.ErrEpisodeSourceConflict
		}
		return episodeAcquisitionResponse{episodeSourceFailure(ctx, e)}, nil
	}
	a := GetActor(ctx)
	if a == nil {
		return fail(ports.ErrUnauthorized)
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return fail(ports.ErrForbidden)
	}
	if s.episodeAcquisitionSvc == nil {
		return fail(ports.ErrUnavailable)
	}
	if r.Body == nil {
		return fail(ports.ErrInvalidEpisodeMapping)
	}
	command := ports.AcquireReviewedEpisodeCommand{SubscriptionID: r.Id, ReviewID: r.Body.ReviewId, ExpectedInputStamp: r.Body.ExpectedInputStamp, Confirmed: bool(r.Body.Confirmed)}
	out, e := s.episodeAcquisitionSvc.AcquireReviewedEpisode(ctx, a, command, r.Params.IdempotencyKey)
	if e != nil {
		return fail(e)
	}
	return episodeAcquisitionResponse{episodeSourceResponse{body: out}}, nil
}
