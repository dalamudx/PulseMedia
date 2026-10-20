package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type siteAccountStatsConfigApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID) (*ports.SiteAccountStatsConfig, error)
	Save(context.Context, *ports.Actor, uuid.UUID, ports.SiteAccountStatsConfigInput, string) (*ports.SiteAccountStatsConfigReceipt, error)
	Receipt(context.Context, *ports.Actor, uuid.UUID, string) (*ports.SiteAccountStatsConfigReceipt, error)
}

func (s *ServerHandler) SetSiteAccountStatsConfigService(service siteAccountStatsConfigApplication) {
	s.siteAccountStatsConfigSvc = service
}

func siteAccountStatsConfigActorError(actor *ports.Actor) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || id.String() != actor.ID || actor.Type != ports.ActorTypeUser || actor.Scopes != nil || actor.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}

func (s *ServerHandler) ReadSiteAccountStatsConfig(ctx context.Context, r ReadSiteAccountStatsConfigRequestObject) (ReadSiteAccountStatsConfigResponseObject, error) {
	if err := siteAccountStatsConfigActorError(GetActor(ctx)); err != nil {
		return siteAccountStatsConfigProblem(ctx, err), nil
	}
	if r.IndexerId == uuid.Nil {
		return siteAccountStatsConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteAccountStatsConfigSvc == nil {
		return siteAccountStatsConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	c, err := s.siteAccountStatsConfigSvc.Read(ctx, GetActor(ctx), r.IndexerId)
	if err != nil {
		return siteAccountStatsConfigProblem(ctx, err), nil
	}
	var out *SiteAccountStatsConfig
	if c != nil {
		value := siteAccountStatsConfigResponse(*c)
		out = &value
	}
	return ReadSiteAccountStatsConfig200JSONResponse{Config: out}, nil
}

func (s *ServerHandler) SaveSiteAccountStatsConfig(ctx context.Context, r SaveSiteAccountStatsConfigRequestObject) (SaveSiteAccountStatsConfigResponseObject, error) {
	if err := siteAccountStatsConfigActorError(GetActor(ctx)); err != nil {
		return siteAccountStatsConfigProblem(ctx, err), nil
	}
	if r.IndexerId == uuid.Nil || r.Body == nil || !validDoubanSyncCommandKey(r.Params.IdempotencyKey) {
		return siteAccountStatsConfigProblem(ctx, ports.ErrRejected), nil
	}
	b := r.Body
	in := ports.SiteAccountStatsConfigInput{ExpectedAccountID: b.ExpectedAccountId, Profile: string(b.Profile), Enabled: b.Enabled, IntervalSeconds: b.IntervalSeconds, ExpectedVersion: b.ExpectedVersion, ExpectedSourceVersion: b.ExpectedSourceVersion}
	if !validSiteAccountStatsConfigInput(in) {
		return siteAccountStatsConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteAccountStatsConfigSvc == nil {
		return siteAccountStatsConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.siteAccountStatsConfigSvc.Save(ctx, GetActor(ctx), r.IndexerId, in, r.Params.IdempotencyKey)
	if err != nil {
		return siteAccountStatsConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return siteAccountStatsConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	return SaveSiteAccountStatsConfig200JSONResponse{CommandKey: receipt.CommandKey, Config: siteAccountStatsConfigResponse(receipt.Config)}, nil
}

func (s *ServerHandler) ReadSiteAccountStatsConfigReceipt(ctx context.Context, r ReadSiteAccountStatsConfigReceiptRequestObject) (ReadSiteAccountStatsConfigReceiptResponseObject, error) {
	if err := siteAccountStatsConfigActorError(GetActor(ctx)); err != nil {
		return siteAccountStatsConfigProblem(ctx, err), nil
	}
	if r.IndexerId == uuid.Nil || !validDoubanSyncCommandKey(r.CommandKey) {
		return siteAccountStatsConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteAccountStatsConfigSvc == nil {
		return siteAccountStatsConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.siteAccountStatsConfigSvc.Receipt(ctx, GetActor(ctx), r.IndexerId, r.CommandKey)
	if err != nil {
		return siteAccountStatsConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return siteAccountStatsConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	return ReadSiteAccountStatsConfigReceipt200JSONResponse{CommandKey: receipt.CommandKey, Config: siteAccountStatsConfigResponse(receipt.Config)}, nil
}

func siteAccountStatsConfigResponse(c ports.SiteAccountStatsConfig) SiteAccountStatsConfig {
	return SiteAccountStatsConfig{IndexerId: c.IndexerID, ExpectedAccountId: c.ExpectedAccountID, Profile: SiteAccountStatsConfigProfile(c.Profile), Enabled: c.Enabled, IntervalSeconds: c.IntervalSeconds, Version: c.Version, SourceConfigVersion: c.SourceConfigVersion, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

type siteAccountStatsConfigErrorResponse struct{ problem ErrorResponse }

func siteAccountStatsConfigProblem(ctx context.Context, err error) siteAccountStatsConfigErrorResponse {
	status, code, message := 503, "SITE_ACCOUNT_STATS_CONFIG_UNAVAILABLE", "account statistics configuration is temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "account statistics configuration access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = 400, "INVALID_REQUEST", "invalid account statistics configuration request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "source or configuration receipt not found"
	case errors.Is(err, ports.ErrEndpointVersionConflict):
		status, code, message = 409, "CONFLICT", "configuration version or command conflicts"
	}
	return siteAccountStatsConfigErrorResponse{problemResponse(status, code, message, GetRequestID(ctx))}
}
func (r siteAccountStatsConfigErrorResponse) VisitReadSiteAccountStatsConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r siteAccountStatsConfigErrorResponse) VisitSaveSiteAccountStatsConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r siteAccountStatsConfigErrorResponse) VisitReadSiteAccountStatsConfigReceiptResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
