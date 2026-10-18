package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"net/http"
)

func (s *ServerHandler) SetMovieSelectionService(service *acquisition.MovieSelectionService) {
	s.movieSelectionSvc = service
}

type movieSelectionResponse struct {
	code int
	body any
}

func (r movieSelectionResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(r.code)
	return json.NewEncoder(w).Encode(r.body)
}
func (r movieSelectionResponse) VisitPreviewSelectedMovieResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r movieSelectionResponse) VisitCommitSelectedMovieResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func movieSelectionFailure(ctx context.Context, e error) movieSelectionResponse {
	code, label := 503, "UNAVAILABLE"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		code, label = 401, "UNAUTHORIZED"
	case errors.Is(e, ports.ErrForbidden):
		code, label = 403, "FORBIDDEN"
	case errors.Is(e, ports.ErrNotFound):
		code, label = 404, "NOT_FOUND"
	case errors.Is(e, ports.ErrInvalidMovieSelection):
		code, label = 400, "INVALID_SELECTION"
	case errors.Is(e, ports.ErrMovieSelectionConflict):
		code, label = 409, "VERSION_CONFLICT"
	case errors.Is(e, ports.ErrAcquisitionUnsupportedCoverage):
		code, label = 409, "UNSUPPORTED_COVERAGE"
	case errors.Is(e, ports.ErrAcquisitionAlreadyOwned), errors.Is(e, ports.ErrAcquisitionUnitAlreadyReserved), errors.Is(e, ports.ErrAcquisitionOwnershipUncertain), errors.Is(e, ports.ErrAcquisitionLocalReuseRequired):
		code, label = 409, "REVIEW_REQUIRED"
	}
	message := "explicit movie selection could not be completed; no new acquisition committed"
	if code == 503 {
		message = "selection result is unconfirmed; retry the same command key"
	}
	return movieSelectionResponse{code, ErrorResponse{Code: label, Message: message, RequestId: GetRequestID(ctx)}}
}
func (s *ServerHandler) PreviewSelectedMovie(ctx context.Context, r PreviewSelectedMovieRequestObject) (PreviewSelectedMovieResponseObject, error) {
	if GetActor(ctx) == nil {
		return movieSelectionFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.movieSelectionSvc == nil {
		return movieSelectionFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return movieSelectionFailure(ctx, ports.ErrInvalidMovieSelection), nil
	}
	raw, _ := json.Marshal(r.Body)
	var c ports.MovieSelection
	if json.Unmarshal(raw, &c) != nil {
		return movieSelectionFailure(ctx, ports.ErrInvalidMovieSelection), nil
	}
	p, e := s.movieSelectionSvc.Preview(ctx, GetActor(ctx), c)
	if e != nil {
		return movieSelectionFailure(ctx, e), nil
	}
	return movieSelectionResponse{200, p}, nil
}
func (s *ServerHandler) CommitSelectedMovie(ctx context.Context, r CommitSelectedMovieRequestObject) (CommitSelectedMovieResponseObject, error) {
	if GetActor(ctx) == nil {
		return movieSelectionFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.movieSelectionSvc == nil {
		return movieSelectionFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return movieSelectionFailure(ctx, ports.ErrInvalidMovieSelection), nil
	}
	raw, _ := json.Marshal(r.Body)
	var c ports.MovieSelectionCommand
	if json.Unmarshal(raw, &c) != nil {
		return movieSelectionFailure(ctx, ports.ErrInvalidMovieSelection), nil
	}
	p, e := s.movieSelectionSvc.Commit(ctx, GetActor(ctx), c, GetIdempotencyKey(ctx))
	if e != nil {
		return movieSelectionFailure(ctx, e), nil
	}
	return movieSelectionResponse{202, p}, nil
}
