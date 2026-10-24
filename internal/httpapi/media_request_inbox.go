package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
	"strconv"
)

type mediaRequestInboxApplication interface {
	Summary(context.Context, *ports.Actor) (*ports.MediaRequestInboxSummary, error)
	Pending(context.Context, *ports.Actor, int, *uuid.UUID) ([]ports.MediaRequest, error)
}

func (s *ServerHandler) GetMediaRequestSummary(ctx context.Context, _ GetMediaRequestSummaryRequestObject) (GetMediaRequestSummaryResponseObject, error) {
	if err := s.mediaRequestAdmission(ctx, true); err != nil {
		return mediaRequestError(ctx, err), nil
	}
	app, ok := s.mediaRequestSvc.(mediaRequestInboxApplication)
	if !ok {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	value, err := app.Summary(ctx, GetActor(ctx))
	if err != nil {
		return mediaRequestError(ctx, err), nil
	}
	if value == nil || value.PendingCount < 0 || value.ObservedAt.IsZero() {
		return mediaRequestError(ctx, ports.ErrUnavailable), nil
	}
	return GetMediaRequestSummary200JSONResponse{PendingCount: strconv.FormatInt(value.PendingCount, 10), ObservedAt: value.ObservedAt.UTC()}, nil
}

func (r mediaRequestFailure) VisitGetMediaRequestSummaryResponse(w http.ResponseWriter) error {
	return r.write(w)
}
