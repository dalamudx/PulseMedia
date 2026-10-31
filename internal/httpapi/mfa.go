package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"net/http"
)

func (s *ServerHandler) SetMFAService(service *auth.MFAService)                    { s.mfaSvc = service }
func (r accountHTTPResult) VisitGetOwnMFAResponse(w http.ResponseWriter) error     { return r.write(w) }
func (r accountHTTPResult) VisitBeginOwnMFAResponse(w http.ResponseWriter) error   { return r.write(w) }
func (r accountHTTPResult) VisitConfirmOwnMFAResponse(w http.ResponseWriter) error { return r.write(w) }
func (r accountHTTPResult) VisitDisableOwnMFAResponse(w http.ResponseWriter) error { return r.write(w) }
func (s *ServerHandler) GetOwnMFA(ctx context.Context, r GetOwnMFARequestObject) (GetOwnMFAResponseObject, error) {
	if s.mfaSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.mfaSvc.Status(ctx, GetActor(ctx), GetRawToken(ctx))
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
func (s *ServerHandler) BeginOwnMFA(ctx context.Context, r BeginOwnMFARequestObject) (BeginOwnMFAResponseObject, error) {
	if s.mfaSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, e := s.mfaSvc.Begin(ctx, GetActor(ctx), GetRawToken(ctx), r.Body.CurrentPassword, r.Params.IdempotencyKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
func (s *ServerHandler) ConfirmOwnMFA(ctx context.Context, r ConfirmOwnMFARequestObject) (ConfirmOwnMFAResponseObject, error) {
	if s.mfaSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, e := s.mfaSvc.Confirm(ctx, GetActor(ctx), GetRawToken(ctx), r.Body.ChallengeId, r.Body.Otp, r.Params.IdempotencyKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
func (s *ServerHandler) DisableOwnMFA(ctx context.Context, r DisableOwnMFARequestObject) (DisableOwnMFAResponseObject, error) {
	if s.mfaSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, e := s.mfaSvc.Disable(ctx, GetActor(ctx), GetRawToken(ctx), ports.MFAReauthentication{CurrentPassword: r.Body.CurrentPassword, MFAProof: accountProof(r.Body.Otp, r.Body.RecoveryCode)}, r.Params.IdempotencyKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
