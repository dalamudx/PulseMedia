package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	siteusecase "github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
)

type siteCheckinHistoryApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID, int32, string) (*siteusecase.CheckinHistoryPage, error)
}

func (s *ServerHandler) SetSiteCheckinHistoryService(service siteCheckinHistoryApplication) {
	s.siteCheckinHistorySvc = service
}

func (s *ServerHandler) ReadSiteCheckinHistory(ctx context.Context, r ReadSiteCheckinHistoryRequestObject) (ReadSiteCheckinHistoryResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return siteCheckinHistoryProblem(ctx, err), nil
	}
	limit := int32(20)
	if r.Params.Limit != nil {
		limit = *r.Params.Limit
	}
	cursor := ""
	if r.Params.Cursor != nil {
		cursor = *r.Params.Cursor
	}
	if r.IndexerId == uuid.Nil || limit < 1 || limit > 100 || (r.Params.Cursor != nil && (cursor == "" || len(cursor) > 512)) {
		return siteCheckinHistoryProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteCheckinHistorySvc == nil {
		return siteCheckinHistoryProblem(ctx, ports.ErrUnavailable), nil
	}
	page, err := s.siteCheckinHistorySvc.Read(ctx, GetActor(ctx), r.IndexerId, limit, cursor)
	if err != nil {
		return siteCheckinHistoryProblem(ctx, err), nil
	}
	if page == nil {
		return siteCheckinHistoryProblem(ctx, ports.ErrUnavailable), nil
	}
	out := SiteCheckinHistory{Rows: make([]SiteCheckinDay, len(page.Rows))}
	if page.Config != nil {
		cfg := siteCheckinConfigResponse(*page.Config)
		out.Config = &cfg
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	for i, row := range page.Rows {
		out.Rows[i] = SiteCheckinDay{Id: row.ID, IndexerId: row.IndexerID, ExpectedAccountId: row.ExpectedAccountID, Profile: SiteCheckinDayProfile(row.Profile), Timezone: row.Timezone, CalendarDay: row.CalendarDay, DayStart: row.DayStart, DayEnd: row.DayEnd, State: SiteCheckinDayState(row.State), SafeFailureCode: SiteCheckinDaySafeFailureCode(row.SafeFailureCode), ObservedAt: row.ObservedAt, Prepared: row.Prepared, ConfigVersion: row.ConfigVersion, SourceVersion: row.SourceVersion, RestoreAcknowledged: row.RestoreAcknowledged}
	}
	return ReadSiteCheckinHistory200JSONResponse(out), nil
}

type siteCheckinHistoryErrorResponse struct{ problem ErrorResponse }

func siteCheckinHistoryProblem(ctx context.Context, err error) siteCheckinHistoryErrorResponse {
	status, code, message := 503, "SITE_CHECKIN_HISTORY_UNAVAILABLE", "site checkin history is temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "site checkin history access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = 400, "INVALID_REQUEST", "invalid site checkin history request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "source not found"
	}
	return siteCheckinHistoryErrorResponse{problemResponse(status, code, message, GetRequestID(ctx))}
}
func (r siteCheckinHistoryErrorResponse) VisitReadSiteCheckinHistoryResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
