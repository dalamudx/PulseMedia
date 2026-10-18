package httpapi

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

func (s *ServerHandler) SetEpisodeMappingService(service *subscription.EpisodeMappingService) {
	s.episodeMappingSvc = service
}

type episodeMappingResponse struct{ episodeSourceResponse }

func (r episodeMappingResponse) VisitGetEpisodeMappingResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r episodeMappingResponse) VisitPreviewEpisodeMappingResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r episodeMappingResponse) VisitPublishEpisodeMappingResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) mappingAuth(ctx context.Context) *episodeMappingResponse {
	a := GetActor(ctx)
	var e error
	if a == nil {
		e = ports.ErrUnauthorized
	} else if !a.IsAdmin() {
		e = ports.ErrForbidden
	} else if s.episodeMappingSvc == nil {
		e = ports.ErrUnavailable
	}
	if e != nil {
		r := episodeMappingResponse{episodeSourceFailure(ctx, e)}
		return &r
	}
	return nil
}
func (s *ServerHandler) GetEpisodeMapping(ctx context.Context, r GetEpisodeMappingRequestObject) (GetEpisodeMappingResponseObject, error) {
	if denied := s.mappingAuth(ctx); denied != nil {
		return *denied, nil
	}
	data, e := s.episodeMappingSvc.Read(ctx, GetActor(ctx), r.Id)
	if e != nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, e)}, nil
	}
	return episodeMappingResponse{episodeSourceResponse{body: data}}, nil
}
func (s *ServerHandler) PreviewEpisodeMapping(ctx context.Context, r PreviewEpisodeMappingRequestObject) (PreviewEpisodeMappingResponseObject, error) {
	if denied := s.mappingAuth(ctx); denied != nil {
		return *denied, nil
	}
	if r.Body == nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, ports.ErrInvalidEpisodeMapping)}, nil
	}
	raw, _ := json.Marshal(r.Body)
	var d ports.EpisodeMappingDraft
	if json.Unmarshal(raw, &d) != nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, ports.ErrInvalidEpisodeMapping)}, nil
	}
	data, e := s.episodeMappingSvc.Preview(ctx, GetActor(ctx), r.Id, d)
	if e != nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, e)}, nil
	}
	return episodeMappingResponse{episodeSourceResponse{body: data}}, nil
}
func (s *ServerHandler) PublishEpisodeMapping(ctx context.Context, r PublishEpisodeMappingRequestObject) (PublishEpisodeMappingResponseObject, error) {
	if denied := s.mappingAuth(ctx); denied != nil {
		return *denied, nil
	}
	if r.Body == nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, ports.ErrInvalidEpisodeMapping)}, nil
	}
	raw, _ := json.Marshal(r.Body)
	var c ports.EpisodeMappingCommand
	if json.Unmarshal(raw, &c) != nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, ports.ErrInvalidEpisodeMapping)}, nil
	}
	data, e := s.episodeMappingSvc.Publish(ctx, GetActor(ctx), r.Id, c, r.Params.IdempotencyKey)
	if e != nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, e)}, nil
	}
	return episodeMappingResponse{episodeSourceResponse{body: data}}, nil
}
