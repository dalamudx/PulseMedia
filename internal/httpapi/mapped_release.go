package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

func (r episodeMappingResponse) VisitExplainMappedReleaseResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) ExplainMappedRelease(ctx context.Context, r ExplainMappedReleaseRequestObject) (ExplainMappedReleaseResponseObject, error) {
	if denied := s.mappingAuth(ctx); denied != nil {
		return *denied, nil
	}
	if r.Body == nil || (r.Body.Name == nil) == (r.Body.CandidateId == nil) {
		return episodeMappingResponse{episodeSourceFailure(ctx, ports.ErrInvalidEpisodeMapping)}, nil
	}
	var out *subscription.MappedReleaseExplanation
	var e error
	if r.Body.CandidateId != nil {
		out, e = s.episodeMappingSvc.ExplainCandidate(ctx, GetActor(ctx), r.Id, *r.Body.CandidateId)
	} else {
		out, e = s.episodeMappingSvc.ExplainRelease(ctx, GetActor(ctx), r.Id, *r.Body.Name)
	}
	if e != nil {
		return episodeMappingResponse{episodeSourceFailure(ctx, e)}, nil
	}
	return episodeMappingResponse{episodeSourceResponse{body: out}}, nil
}
