package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
)

func (r accountHTTPResult) VisitRegenerateOwnMFARecoveryCodesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *ServerHandler) RegenerateOwnMFARecoveryCodes(ctx context.Context, r RegenerateOwnMFARecoveryCodesRequestObject) (RegenerateOwnMFARecoveryCodesResponseObject, error) {
	if s.mfaSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, err := s.mfaSvc.RegenerateRecoveryCodes(ctx, GetActor(ctx), GetRawToken(ctx), ports.MFAReauthentication{CurrentPassword: r.Body.CurrentPassword, MFAProof: accountProof(r.Body.Otp, r.Body.RecoveryCode)}, r.Params.IdempotencyKey)
	if err != nil {
		return accountHTTPFailure(ctx, err), nil
	}
	return accountHTTPResult{http.StatusOK, out}, nil
}
