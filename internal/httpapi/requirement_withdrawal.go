package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"net/http"
)

func (s *ServerHandler) SetWithdrawalService(service *acquisition.WithdrawalService) {
	s.withdrawSvc = service
}

type withdrawalHTTPError struct {
	status int
	body   ErrorResponse
}

func withdrawalFailure(ctx context.Context, e error) withdrawalHTTPError {
	status, code := 503, "UNAVAILABLE"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		status, code = 401, "UNAUTHORIZED"
	case errors.Is(e, ports.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(e, ports.ErrRequirementConflict) || errors.Is(e, ports.ErrDownloadDemandChanged):
		status, code = 409, "REQUIREMENT_CHANGED"
	}
	return withdrawalHTTPError{status, ErrorResponse{Code: code, Message: "requirement request could not be completed", RequestId: GetRequestID(ctx)}}
}
func (e withdrawalHTTPError) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(e.status)
	return json.NewEncoder(w).Encode(e.body)
}
func (e withdrawalHTTPError) VisitListOwnOperationRequirementsResponse(w http.ResponseWriter) error {
	return e.write(w)
}
func (e withdrawalHTTPError) VisitWithdrawOwnRequirementResponse(w http.ResponseWriter) error {
	return e.write(w)
}
func (s *ServerHandler) ListOwnOperationRequirements(ctx context.Context, r ListOwnOperationRequirementsRequestObject) (ListOwnOperationRequirementsResponseObject, error) {
	if s.withdrawSvc == nil {
		return withdrawalFailure(ctx, ports.ErrUnavailable), nil
	}
	rows, e := s.withdrawSvc.List(ctx, GetActor(ctx), r.Id)
	if e != nil {
		return withdrawalFailure(ctx, e), nil
	}
	items := []RequirementDetails{}
	for _, row := range rows {
		items = append(items, RequirementDetails{Id: row.ID, OperationId: row.OperationID, UnitId: row.UnitID, RootId: row.RootID, State: RequirementDetailsState(row.State), FenceDigest: row.FenceDigest})
	}
	return ListOwnOperationRequirements200JSONResponse{Items: items}, nil
}
func (s *ServerHandler) WithdrawOwnRequirement(ctx context.Context, r WithdrawOwnRequirementRequestObject) (WithdrawOwnRequirementResponseObject, error) {
	if s.withdrawSvc == nil {
		return withdrawalFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return withdrawalFailure(ctx, ports.ErrRequirementConflict), nil
	}
	result, e := s.withdrawSvc.Withdraw(ctx, GetActor(ctx), ports.RequirementWithdrawalCommand{OperationID: r.Id, RequirementID: r.RequirementId, ExpectedFence: r.Body.ExpectedFence, Key: r.Params.IdempotencyKey})
	if e != nil {
		return withdrawalFailure(ctx, e), nil
	}
	return WithdrawOwnRequirement200JSONResponse{OperationId: result.OperationID, RequirementId: result.RequirementID, State: RequirementWithdrawalReceiptStateReleased, PhysicalReleased: result.PhysicalReleased}, nil
}
