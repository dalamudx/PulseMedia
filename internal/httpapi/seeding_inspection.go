package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
)

func (s *ServerHandler) SetSeedingInspectionService(service *download.SeedingInspectionService) {
	s.seedingInspectionSvc = service
}

func (s *ServerHandler) ObserveOperationSeeding(ctx context.Context, r ObserveOperationSeedingRequestObject) (ObserveOperationSeedingResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return seedingInspectionProblem(ctx, ports.ErrUnauthorized), nil
	}
	if actor.Type != ports.ActorTypeUser || actor.Scopes != nil {
		return seedingInspectionProblem(ctx, ports.ErrForbidden), nil
	}
	if s.seedingInspectionSvc == nil {
		return seedingInspectionProblem(ctx, ports.ErrUnavailable), nil
	}
	result, err := s.seedingInspectionSvc.Inspect(ctx, actor, r.Id)
	if err != nil {
		return seedingInspectionProblem(ctx, err), nil
	}
	if result == nil {
		return seedingInspectionProblem(ctx, ports.ErrUnavailable), nil
	}
	return seedingInspectionSuccess{*result}, nil
}

type seedingInspectionSuccess struct{ result ports.SeedingInspection }

func (r seedingInspectionSuccess) VisitObserveOperationSeedingResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(r.result)
}

type seedingInspectionHTTPError struct{ problem ErrorResponse }

func seedingInspectionProblem(ctx context.Context, err error) seedingInspectionHTTPError {
	status, code := http.StatusServiceUnavailable, "SEEDING_INSPECTION_UNAVAILABLE"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code = http.StatusUnauthorized, "UNAUTHORIZED"
	case errors.Is(err, ports.ErrForbidden):
		status, code = http.StatusForbidden, "FORBIDDEN"
	case errors.Is(err, ports.ErrNotFound):
		status, code = http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, ports.ErrDownloadControlInvalid):
		status, code = http.StatusBadRequest, "INVALID_SEEDING_INSPECTION"
	case errors.Is(err, ports.ErrDownloadControlConflict), errors.Is(err, ports.ErrDownloadControlUnsupported), errors.Is(err, ports.ErrUpgradeNotExecutable), errors.Is(err, ports.ErrCompleteTVNeedsObservation), errors.Is(err, ports.ErrAutomaticTVNeedsObservation):
		status, code = http.StatusConflict, "SEEDING_INSPECTION_UNSUPPORTED"
	}
	return seedingInspectionHTTPError{problemResponse(status, code, "seeding information could not be read; refresh the operation and try again", GetRequestID(ctx))}
}
func (r seedingInspectionHTTPError) VisitObserveOperationSeedingResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
