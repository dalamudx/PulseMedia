package httpapi

import (
	"context"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func (s *ServerHandler) SetMovieDiscoveryService(service *usecaseSearch.MovieDiscoveryService) {
	s.movieDiscoverySvc = service
}

type movieDiscoveryHTTPResponse struct{ interactiveSearchHTTPResponse }

func (r movieDiscoveryHTTPResponse) VisitGetMovieDiscoveryResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *ServerHandler) GetMovieDiscovery(ctx context.Context, r GetMovieDiscoveryRequestObject) (GetMovieDiscoveryResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return movieDiscoveryHTTPResponse{interactiveSearchFailure(ctx, ports.ErrUnauthorized)}, nil
	}
	if s.movieDiscoverySvc == nil {
		return movieDiscoveryHTTPResponse{interactiveSearchFailure(ctx, ports.ErrUnavailable)}, nil
	}
	status, err := s.movieDiscoverySvc.Read(ctx, actor, r.Id)
	if err != nil {
		return movieDiscoveryHTTPResponse{interactiveSearchFailure(ctx, err)}, nil
	}
	return movieDiscoveryHTTPResponse{interactiveSearchHTTPResponse{status: http.StatusOK, body: status}}, nil
}
