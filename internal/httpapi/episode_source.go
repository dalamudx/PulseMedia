package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

func (s *ServerHandler) SetEpisodeSourceService(service *subscription.EpisodeSourceService) {
	s.episodeSourceSvc = service
}

type episodeSourceResponse struct {
	body    any
	failure *controlFailure
}

func (r episodeSourceResponse) write(w http.ResponseWriter) error {
	if r.failure != nil {
		return r.failure.VisitControlSubscriptionResponse(w)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(r.body)
}
func (r episodeSourceResponse) VisitGetTVEpisodeGroupsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r episodeSourceResponse) VisitPreviewTVEpisodeGroupResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func episodeSourceFailure(ctx context.Context, e error) episodeSourceResponse {
	if errors.Is(e, ports.ErrInvalidEpisodeMapping) {
		e = ports.ErrInvalidSubscriptionBindings
	}
	if errors.Is(e, ports.ErrEpisodeSourceConflict) {
		e = ports.ErrSubscriptionBindingsConflict
	}
	f := failedControl(ctx, e)
	return episodeSourceResponse{failure: &f}
}
func (s *ServerHandler) GetTVEpisodeGroups(ctx context.Context, r GetTVEpisodeGroupsRequestObject) (GetTVEpisodeGroupsResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return episodeSourceFailure(ctx, ports.ErrUnauthorized), nil
	}
	if !a.IsAdmin() {
		return episodeSourceFailure(ctx, ports.ErrForbidden), nil
	}
	if s.episodeSourceSvc == nil {
		return episodeSourceFailure(ctx, ports.ErrUnavailable), nil
	}
	data, e := s.episodeSourceSvc.List(ctx, a, r.Id)
	if e != nil {
		return episodeSourceFailure(ctx, e), nil
	}
	return episodeSourceResponse{body: data}, nil
}
func (s *ServerHandler) PreviewTVEpisodeGroup(ctx context.Context, r PreviewTVEpisodeGroupRequestObject) (PreviewTVEpisodeGroupResponseObject, error) {
	a := GetActor(ctx)
	if a == nil {
		return episodeSourceFailure(ctx, ports.ErrUnauthorized), nil
	}
	if !a.IsAdmin() {
		return episodeSourceFailure(ctx, ports.ErrForbidden), nil
	}
	if s.episodeSourceSvc == nil {
		return episodeSourceFailure(ctx, ports.ErrUnavailable), nil
	}
	part := ""
	if r.Params.PartId != nil {
		part = *r.Params.PartId
	}
	data, e := s.episodeSourceSvc.Preview(ctx, a, r.Id, r.Params.GroupId, part)
	if e != nil {
		return episodeSourceFailure(ctx, e), nil
	}
	return episodeSourceResponse{body: data}, nil
}
