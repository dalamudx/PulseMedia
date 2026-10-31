package httpapi

import (
	"context"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (r accountHTTPResult) VisitRevokeOtherSessionsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (s *ServerHandler) RevokeOtherSessions(ctx context.Context, r RevokeOtherSessionsRequestObject) (RevokeOtherSessionsResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return accountHTTPFailure(ctx, ports.ErrInvalidAccountCommand), nil
	}
	out, err := s.accountManagementSvc.RevokeOtherSessions(ctx, GetActor(ctx), GetRawToken(ctx), ports.MFAReauthentication{CurrentPassword: r.Body.CurrentPassword, MFAProof: accountProof(r.Body.Otp, r.Body.RecoveryCode)}, r.Params.IdempotencyKey)
	if err != nil {
		return accountHTTPFailure(ctx, err), nil
	}
	return accountHTTPResult{http.StatusOK, out}, nil
}
