package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"net/http"
)

func (s *ServerHandler) SetAccountManagementService(service *auth.AccountManagementService) {
	s.accountManagementSvc = service
}

type accountHTTPResult struct {
	status int
	body   any
}

func (r accountHTTPResult) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	return json.NewEncoder(w).Encode(r.body)
}
func (r accountHTTPResult) VisitListAccountsResponse(w http.ResponseWriter) error  { return r.write(w) }
func (r accountHTTPResult) VisitCreateAccountResponse(w http.ResponseWriter) error { return r.write(w) }
func (r accountHTTPResult) VisitUpdateAccountResponse(w http.ResponseWriter) error { return r.write(w) }
func (r accountHTTPResult) VisitListOwnSessionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r accountHTTPResult) VisitRevokeOwnSessionResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r accountHTTPResult) VisitChangeOwnPasswordResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func accountHTTPFailure(ctx context.Context, e error) accountHTTPResult {
	f := failedControl(ctx, e)
	switch {
	case errors.Is(e, ports.ErrInvalidAccountCommand):
		f.status = 400
		f.body.Code = "INVALID_ACCOUNT_COMMAND"
	case errors.Is(e, ports.ErrInvalidCredentials):
		f.status = 400
		f.body.Code = "REAUTHENTICATION_FAILED"
	case errors.Is(e, ports.ErrAccountCommandConflict):
		f.status = 409
		f.body.Code = "ACCOUNT_COMMAND_CONFLICT"
	case errors.Is(e, ports.ErrLastActiveAdmin):
		f.status = 409
		f.body.Code = "LAST_ACTIVE_ADMIN"
	}
	f.body.Message = "account security request could not be completed"
	return accountHTTPResult{f.status, f.body}
}
func accountProof(otp, recovery *string) ports.MFAProof {
	p := ports.MFAProof{}
	if otp != nil {
		p.OTP = *otp
	}
	if recovery != nil {
		p.RecoveryCode = *recovery
	}
	return p
}
func (s *ServerHandler) ListAccounts(ctx context.Context, r ListAccountsRequestObject) (ListAccountsResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.accountManagementSvc.List(ctx, GetActor(ctx), GetRawToken(ctx))
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, map[string]any{"items": out}}, nil
}
func (s *ServerHandler) CreateAccount(ctx context.Context, r CreateAccountRequestObject) (CreateAccountResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, e := s.accountManagementSvc.Create(ctx, GetActor(ctx), GetRawToken(ctx), ports.AccountCreate{Username: r.Body.Username, Password: r.Body.Password, Role: string(r.Body.Role)}, r.Params.IdempotencyKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
func (s *ServerHandler) UpdateAccount(ctx context.Context, r UpdateAccountRequestObject) (UpdateAccountResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, e := s.accountManagementSvc.Update(ctx, GetActor(ctx), GetRawToken(ctx), r.Id, ports.AccountUpdate{Role: string(r.Body.Role), IsActive: r.Body.IsActive}, r.Params.IdempotencyKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
func (s *ServerHandler) ListOwnSessions(ctx context.Context, r ListOwnSessionsRequestObject) (ListOwnSessionsResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.accountManagementSvc.Sessions(ctx, GetActor(ctx), GetRawToken(ctx))
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, map[string]any{"items": out}}, nil
}
func (s *ServerHandler) RevokeOwnSession(ctx context.Context, r RevokeOwnSessionRequestObject) (RevokeOwnSessionResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.accountManagementSvc.RevokeSession(ctx, GetActor(ctx), GetRawToken(ctx), r.Id, r.Params.IdempotencyKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
func (s *ServerHandler) ChangeOwnPassword(ctx context.Context, r ChangeOwnPasswordRequestObject) (ChangeOwnPasswordResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, e := s.accountManagementSvc.ChangePassword(ctx, GetActor(ctx), GetRawToken(ctx), ports.PasswordChange{CurrentPassword: r.Body.CurrentPassword, NewPassword: r.Body.NewPassword, MFAProof: accountProof(r.Body.Otp, r.Body.RecoveryCode)}, r.Params.IdempotencyKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
