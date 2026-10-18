package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/http"
)

type legacyControlResponse struct {
	status int
	body   ErrorResponse
}

func (s *ServerHandler) legacyControl(ctx context.Context, id uuid.UUID) legacyControlResponse {
	status, code, message := 409, "VERSIONED_COMMAND_REQUIRED", "use PUT subscription control with expected_version and Idempotency-Key; history and external actions are preserved"
	a := GetActor(ctx)
	if a == nil {
		status, code, message = 401, "UNAUTHORIZED", "authentication required"
	} else if s.app.Queries() == nil {
		status, code, message = 503, "UNAVAILABLE", "subscription scope unavailable"
	} else {
		sub, e := s.app.Queries().GetSubscriptionByID(ctx, id)
		if errors.Is(e, pgx.ErrNoRows) {
			status, code, message = 404, "NOT_FOUND", "subscription not found"
		} else if e != nil {
			status, code, message = 503, "UNAVAILABLE", "subscription scope unavailable"
		} else if !a.CanAccessUser(sub.UserID.String()) {
			status, code, message = 403, "FORBIDDEN", "subscription not accessible"
		}
	}
	return legacyControlResponse{status, ErrorResponse{Code: code, Message: message, RequestId: GetRequestID(ctx)}}
}
func (e legacyControlResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	return json.NewEncoder(w).Encode(e.body)
}
func (e legacyControlResponse) VisitPauseSubscriptionResponse(w http.ResponseWriter) error {
	return e.write(w)
}
func (e legacyControlResponse) VisitResumeSubscriptionResponse(w http.ResponseWriter) error {
	return e.write(w)
}
func (e legacyControlResponse) VisitDeleteSubscriptionResponse(w http.ResponseWriter) error {
	return e.write(w)
}
