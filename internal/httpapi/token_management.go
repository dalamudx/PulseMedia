package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"net/http"
)

func (s *ServerHandler) SetTokenManagementService(v *auth.TokenManagementService) {
	s.tokenManagementSvc = v
}

type tokenHTTPResult struct {
	status int
	body   any
}

func (r tokenHTTPResult) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	return json.NewEncoder(w).Encode(r.body)
}
func (r tokenHTTPResult) VisitListOwnTokensResponse(w http.ResponseWriter) error  { return r.write(w) }
func (r tokenHTTPResult) VisitCreateOwnTokenResponse(w http.ResponseWriter) error { return r.write(w) }
func (r tokenHTTPResult) VisitRevokeOwnTokenResponse(w http.ResponseWriter) error { return r.write(w) }
func tokenHTTPFailure(ctx context.Context, e error) tokenHTTPResult {
	f := failedControl(ctx, e)
	if errors.Is(e, ports.ErrInvalidTokenCommand) {
		f.status = 400
		f.body.Code = "INVALID_TOKEN_COMMAND"
	}
	if errors.Is(e, ports.ErrTokenCommandConflict) {
		f.status = 409
		f.body.Code = "TOKEN_COMMAND_CONFLICT"
	}
	f.body.Message = "token request could not be completed"
	return tokenHTTPResult{f.status, f.body}
}
func (s *ServerHandler) ListOwnTokens(ctx context.Context, r ListOwnTokensRequestObject) (ListOwnTokensResponseObject, error) {
	if s.tokenManagementSvc == nil {
		return tokenHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.tokenManagementSvc.List(ctx, GetActor(ctx))
	if e != nil {
		return tokenHTTPFailure(ctx, e), nil
	}
	return tokenHTTPResult{200, map[string]any{"items": out}}, nil
}
func (s *ServerHandler) CreateOwnToken(ctx context.Context, r CreateOwnTokenRequestObject) (CreateOwnTokenResponseObject, error) {
	if s.tokenManagementSvc == nil {
		return tokenHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return tokenHTTPFailure(ctx, ports.ErrInvalidTokenCommand), nil
	}
	out, plain, e := s.tokenManagementSvc.Create(ctx, GetActor(ctx), ports.TokenCommand{Name: r.Body.Name, ExpiresAt: r.Body.ExpiresAt}, r.Params.IdempotencyKey)
	if e != nil {
		return tokenHTTPFailure(ctx, e), nil
	}
	body := struct {
		*ports.TokenCommandResult
		Secret string `json:"secret,omitempty"`
	}{out, plain}
	return tokenHTTPResult{200, body}, nil
}
func (s *ServerHandler) RevokeOwnToken(ctx context.Context, r RevokeOwnTokenRequestObject) (RevokeOwnTokenResponseObject, error) {
	if s.tokenManagementSvc == nil {
		return tokenHTTPFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.tokenManagementSvc.Revoke(ctx, GetActor(ctx), r.Id, r.Params.IdempotencyKey)
	if e != nil {
		return tokenHTTPFailure(ctx, e), nil
	}
	return tokenHTTPResult{200, out}, nil
}
