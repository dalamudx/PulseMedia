package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
)

func (r accountHTTPResult) VisitGetOwnSecurityCommandResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (s *ServerHandler) GetOwnSecurityCommand(ctx context.Context, r GetOwnSecurityCommandRequestObject) (GetOwnSecurityCommandResponseObject, error) {
	if s.accountManagementSvc == nil {
		return accountHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.accountManagementSvc.ReadCommand(ctx, GetActor(ctx), GetRawToken(ctx), r.CommandKey)
	if e != nil {
		return accountHTTPFailure(ctx, e), nil
	}
	return accountHTTPResult{200, out}, nil
}
