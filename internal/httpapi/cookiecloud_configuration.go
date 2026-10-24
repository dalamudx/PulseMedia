package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type cookieCloudConfigApplication interface {
	Read(context.Context, *ports.Actor) (*ports.CookieCloudConfigView, error)
	Save(context.Context, *ports.Actor, ports.CookieCloudConfigInput, string) (*ports.CookieCloudConfigReceipt, error)
	Receipt(context.Context, *ports.Actor, string) (*ports.CookieCloudConfigReceipt, error)
}

func (s *ServerHandler) SetCookieCloudConfigService(service cookieCloudConfigApplication) {
	s.cookieCloudConfigSvc = service
}
func cookieCloudConfigResponse(c ports.CookieCloudConfigView) CookieCloudConfig {
	return CookieCloudConfig{OwnerId: c.OwnerID, Version: c.Version, Enabled: c.Enabled, IntervalSeconds: c.IntervalSeconds, ServerOrigin: c.ServerOrigin, HasConnection: c.HasConnection, UpdatedAt: c.UpdatedAt}
}
func cookieCloudConfigReceiptResponse(r *ports.CookieCloudConfigReceipt) CookieCloudConfigReceipt {
	return CookieCloudConfigReceipt{CommandKey: r.CommandKey, Config: cookieCloudConfigResponse(r.Config)}
}
func (s *ServerHandler) ReadCookieCloudConfig(ctx context.Context, _ ReadCookieCloudConfigRequestObject) (ReadCookieCloudConfigResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return cookieCloudConfigProblem(ctx, err), nil
	}
	if s.cookieCloudConfigSvc == nil {
		return cookieCloudConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	record, err := s.cookieCloudConfigSvc.Read(ctx, GetActor(ctx))
	if err != nil {
		return cookieCloudConfigProblem(ctx, err), nil
	}
	var config *CookieCloudConfig
	if record != nil {
		value := cookieCloudConfigResponse(*record)
		config = &value
	}
	return ReadCookieCloudConfig200JSONResponse{Config: config}, nil
}
func (s *ServerHandler) SaveCookieCloudConfig(ctx context.Context, r SaveCookieCloudConfigRequestObject) (SaveCookieCloudConfigResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return cookieCloudConfigProblem(ctx, err), nil
	}
	if r.Body == nil || !validCookieCloudCommandKey(r.Params.IdempotencyKey) {
		return cookieCloudConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.cookieCloudConfigSvc == nil {
		return cookieCloudConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	input := ports.CookieCloudConfigInput{ExpectedVersion: r.Body.ExpectedVersion, Enabled: r.Body.Enabled, IntervalSeconds: r.Body.IntervalSeconds}
	if r.Body.Connection != nil {
		c := r.Body.Connection
		if c.ServerUrl == nil || c.UserKey == nil || c.Password == nil {
			return cookieCloudConfigProblem(ctx, ports.ErrRejected), nil
		}
		input.Connection = &ports.CookieCloudConnection{ServerURL: *c.ServerUrl, UserKey: *c.UserKey, Password: *c.Password}
	}
	receipt, err := s.cookieCloudConfigSvc.Save(ctx, GetActor(ctx), input, r.Params.IdempotencyKey)
	if err != nil {
		return cookieCloudConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return cookieCloudConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	return SaveCookieCloudConfig200JSONResponse(cookieCloudConfigReceiptResponse(receipt)), nil
}
func (s *ServerHandler) ReadCookieCloudConfigReceipt(ctx context.Context, r ReadCookieCloudConfigReceiptRequestObject) (ReadCookieCloudConfigReceiptResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return cookieCloudConfigProblem(ctx, err), nil
	}
	if !validCookieCloudCommandKey(r.CommandKey) {
		return cookieCloudConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.cookieCloudConfigSvc == nil {
		return cookieCloudConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.cookieCloudConfigSvc.Receipt(ctx, GetActor(ctx), r.CommandKey)
	if err != nil {
		return cookieCloudConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return cookieCloudConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	return ReadCookieCloudConfigReceipt200JSONResponse(cookieCloudConfigReceiptResponse(receipt)), nil
}

type cookieCloudConfigErrorResponse struct{ problem ErrorResponse }

func cookieCloudConfigProblem(ctx context.Context, err error) cookieCloudConfigErrorResponse {
	status, code, message := http.StatusServiceUnavailable, "COOKIECLOUD_CONFIG_UNAVAILABLE", "CookieCloud configuration is temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = http.StatusForbidden, "FORBIDDEN", "CookieCloud configuration access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "invalid CookieCloud configuration request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "configuration receipt not found"
	case errors.Is(err, ports.ErrCookieCloudConfigConflict):
		status, code, message = http.StatusConflict, "CONFLICT", "configuration version or original command conflicts"
	}
	return cookieCloudConfigErrorResponse{problem: problemResponse(status, code, message, GetRequestID(ctx))}
}
func (r cookieCloudConfigErrorResponse) VisitReadCookieCloudConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r cookieCloudConfigErrorResponse) VisitSaveCookieCloudConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r cookieCloudConfigErrorResponse) VisitReadCookieCloudConfigReceiptResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
