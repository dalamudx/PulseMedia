package httpapi

import (
	"context"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (r accountHTTPResult) VisitResetAccountPasswordResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *ServerHandler) ResetAccountPassword(ctx context.Context, r ResetAccountPasswordRequestObject) (ResetAccountPasswordResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, err := s.accountManagementSvc.ResetMemberPassword(ctx, GetActor(ctx), GetRawToken(ctx), r.Id, ports.PasswordChange{
		CurrentPassword: r.Body.CurrentPassword,
		NewPassword:     r.Body.NewPassword,
		MFAProof:        accountProof(r.Body.Otp, r.Body.RecoveryCode),
	}, r.Params.IdempotencyKey)
	if err != nil {
		return accountHTTPFailure(ctx, err), nil
	}
	return accountHTTPResult{http.StatusOK, out}, nil
}
