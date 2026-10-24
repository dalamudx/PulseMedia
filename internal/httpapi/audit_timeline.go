package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type auditTimelineApplication interface {
	List(context.Context, *ports.Actor, ports.AuditTimelineRequest) (*ports.AuditTimelinePage, error)
}

func (s *ServerHandler) SetAuditTimelineService(service auditTimelineApplication) {
	s.auditTimelineSvc = service
}

func auditTimelineReadAllowed(a *ports.Actor) bool {
	return a != nil && a.Type == ports.ActorTypeUser && a.IsAdmin() && (a.Scopes == nil || len(a.Scopes) == 1 && a.Scopes[0] == "read")
}

func (s *ServerHandler) ListAuditTimeline(ctx context.Context, request ListAuditTimelineRequestObject) (ListAuditTimelineResponseObject, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return auditTimelineHTTPError(ctx, ports.ErrUnauthorized), nil
	}
	if !auditTimelineReadAllowed(actor) {
		return auditTimelineHTTPError(ctx, ports.ErrForbidden), nil
	}
	if s.auditTimelineSvc == nil {
		return auditTimelineHTTPError(ctx, ports.ErrUnavailable), nil
	}
	var query ports.AuditTimelineRequest
	p := request.Params
	if p.Limit != nil {
		query.Limit = *p.Limit
	}
	if p.Cursor != nil {
		query.Cursor = *p.Cursor
	}
	if p.ActorType != nil {
		query.Filter.ActorType = string(*p.ActorType)
	}
	if p.ActorId != nil {
		query.Filter.ActorID = *p.ActorId
	}
	if p.Action != nil {
		query.Filter.Action = *p.Action
	}
	if p.TargetType != nil {
		query.Filter.TargetType = *p.TargetType
	}
	if p.TargetId != nil {
		query.Filter.TargetID = *p.TargetId
	}
	if p.From != nil {
		query.Filter.From = *p.From
	}
	if p.Until != nil {
		query.Filter.Until = *p.Until
	}
	page, err := s.auditTimelineSvc.List(ctx, actor, query)
	if err != nil {
		return auditTimelineHTTPError(ctx, err), nil
	}
	if page == nil {
		return auditTimelineHTTPError(ctx, ports.ErrUnavailable), nil
	}
	out := AuditTimelinePage{Items: make([]AuditTimelineEntry, 0, len(page.Items))}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	for _, row := range page.Items {
		out.Items = append(out.Items, AuditTimelineEntry{Id: row.ID, ActorType: row.ActorType, ActorId: row.ActorID, Action: row.Action, TargetType: row.TargetType, TargetId: row.TargetID, CreatedAt: row.CreatedAt.UTC()})
	}
	cacheControl := "private, no-store"
	return ListAuditTimeline200JSONResponse{Body: out, Headers: ListAuditTimeline200ResponseHeaders{CacheControl: &cacheControl}}, nil
}

func auditTimelineHTTPError(ctx context.Context, err error) ListAuditTimelineResponseObject {
	id := GetRequestID(ctx)
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		return ListAuditTimeline401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id))
	case errors.Is(err, ports.ErrForbidden):
		return ListAuditTimeline403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "current administrator access required", id))
	case errors.Is(err, ports.ErrRejected):
		return ListAuditTimeline400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "invalid audit filter or cursor", id))
	default:
		return ListAuditTimeline503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "audit timeline unavailable", id))
	}
}
