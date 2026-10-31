package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// systemLogsResponse streams live process logs after the strict layer has
// authorized the request. It carries the caller's context so cancellation
// closes the stream.
type systemLogsResponse struct {
	hub        *SSEHub
	ctx        context.Context
	level      slog.Level
	authorized func(context.Context) bool
}

func (r systemLogsResponse) VisitStreamSystemLogsResponse(w http.ResponseWriter) error {
	request := (&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/system/logs"}}).WithContext(r.ctx)
	r.hub.ServeLogs(w, request, r.level, r.authorized)
	return nil
}

// StreamSystemLogs serves /api/v1/system/logs. Process-wide logs have no
// per-object owner, so only current administrator sessions may read them.
func (s *ServerHandler) StreamSystemLogs(ctx context.Context, request StreamSystemLogsRequestObject) (StreamSystemLogsResponseObject, error) {
	actor := GetActor(ctx)
	requestID := GetRequestID(ctx)
	if actor == nil {
		return StreamSystemLogs401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", requestID)), nil
	}
	if actor.Type != ports.ActorTypeUser || !actor.IsAdmin() || actor.Scopes != nil {
		return StreamSystemLogs403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "administrator access required", requestID)), nil
	}
	if s.logHub == nil {
		return StreamSystemLogs503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "log stream unavailable", requestID)), nil
	}
	minimum := slog.LevelInfo
	if request.Params.Level != nil {
		minimum = ParseLogLevel(string(*request.Params.Level))
	}
	return systemLogsResponse{hub: s.logHub, ctx: ctx, level: minimum, authorized: func(check context.Context) bool {
		if s.authSvc == nil {
			return false
		}
		current, err := s.authSvc.AuthenticateToken(check, GetRawToken(ctx))
		return err == nil && current != nil && current.Type == ports.ActorTypeUser && current.ID == actor.ID && current.IsAdmin() && current.Scopes == nil
	}}, nil
}

// SetLogHub injects the process-wide hub used for the live log stream.
func (s *ServerHandler) SetLogHub(hub *SSEHub) { s.logHub = hub }
