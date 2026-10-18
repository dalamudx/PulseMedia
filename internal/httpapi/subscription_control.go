package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"net/http"
)

func (s *ServerHandler) SetControlService(service *subscription.ControlService) {
	s.controlSvc = service
}

type controlFailure struct {
	status int
	body   ErrorResponse
}

func (e controlFailure) VisitControlSubscriptionResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	return json.NewEncoder(w).Encode(e.body)
}
func failedControl(ctx context.Context, e error) controlFailure {
	status, code := 503, "UNAVAILABLE"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		status, code = 401, "UNAUTHORIZED"
	case errors.Is(e, ports.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(e, ports.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(e, ports.ErrInvalidSubscriptionBindings):
		status, code = 400, "INVALID_CONTROL"
	case errors.Is(e, ports.ErrSubscriptionBindingsConflict):
		status, code = 409, "VERSION_CONFLICT"
	}
	return controlFailure{status, ErrorResponse{Code: code, Message: "subscription control request could not be completed", RequestId: GetRequestID(ctx)}}
}
func (s *ServerHandler) ControlSubscription(ctx context.Context, r ControlSubscriptionRequestObject) (ControlSubscriptionResponseObject, error) {
	if s.controlSvc == nil {
		return failedControl(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return failedControl(ctx, ports.ErrInvalidSubscriptionBindings), nil
	}
	state := ""
	if r.Body.State != nil {
		state = string(*r.Body.State)
	}
	var scopeType *string
	if r.Body.ScopeType != nil {
		value := string(*r.Body.ScopeType)
		scopeType = &value
	}
	result, e := s.controlSvc.Apply(ctx, GetActor(ctx), r.Id, ports.SubscriptionControlCommand{Action: string(r.Body.Action), ExpectedVersion: r.Body.ExpectedVersion, State: state, UnitID: r.Body.UnitId, Skipped: r.Body.Skipped, CandidateID: r.Body.CandidateId, Blocked: r.Body.Blocked, ScopeType: scopeType, Seasons: r.Body.Seasons, IncludeSpecials: r.Body.IncludeSpecials, SelectedUnitIDs: r.Body.SelectedUnitIds}, r.Params.IdempotencyKey)
	if e != nil {
		return failedControl(ctx, e), nil
	}
	return controlSuccess{ControlSubscription200JSONResponse{Id: result.ID, State: SubscriptionControlReceiptState(result.State), IntentVersion: result.IntentVersion, UnitId: result.UnitID, Skipped: result.Skipped, CandidateId: result.CandidateID, Blocked: result.Blocked}}, nil
}
