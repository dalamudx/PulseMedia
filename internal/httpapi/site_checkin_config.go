package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type siteCheckinConfigApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID) (*ports.SiteCheckinConfig, error)
	Save(context.Context, *ports.Actor, uuid.UUID, ports.SiteCheckinConfigInput, string) (*ports.SiteCheckinConfigReceipt, error)
	Receipt(context.Context, *ports.Actor, uuid.UUID, string) (*ports.SiteCheckinConfigReceipt, error)
}

func (s *ServerHandler) SetSiteCheckinConfigService(service siteCheckinConfigApplication) {
	s.siteCheckinConfigSvc = service
}

func siteCheckinConfigActorError(actor *ports.Actor) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil || id.String() != actor.ID || actor.Type != ports.ActorTypeUser || actor.Scopes != nil || actor.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}

func (s *ServerHandler) ReadSiteCheckinConfig(ctx context.Context, r ReadSiteCheckinConfigRequestObject) (ReadSiteCheckinConfigResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return siteCheckinConfigProblem(ctx, err), nil
	}
	if r.IndexerId == uuid.Nil {
		return siteCheckinConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteCheckinConfigSvc == nil {
		return siteCheckinConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	c, err := s.siteCheckinConfigSvc.Read(ctx, GetActor(ctx), r.IndexerId)
	if err != nil {
		return siteCheckinConfigProblem(ctx, err), nil
	}
	var out *SiteCheckinConfig
	if c != nil {
		value := siteCheckinConfigResponse(*c)
		out = &value
	}
	return ReadSiteCheckinConfig200JSONResponse{Config: out}, nil
}

func (s *ServerHandler) SaveSiteCheckinConfig(ctx context.Context, r SaveSiteCheckinConfigRequestObject) (SaveSiteCheckinConfigResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return siteCheckinConfigProblem(ctx, err), nil
	}
	if r.IndexerId == uuid.Nil || r.Body == nil || !validDoubanSyncCommandKey(r.Params.IdempotencyKey) {
		return siteCheckinConfigProblem(ctx, ports.ErrRejected), nil
	}
	b := r.Body
	in := ports.SiteCheckinConfigInput{ExpectedAccountID: b.ExpectedAccountId, Profile: string(b.Profile), Enabled: b.Enabled, Timezone: b.Timezone, DailyAt: b.DailyAt, ExpectedVersion: b.ExpectedVersion, ExpectedSourceVersion: b.ExpectedSourceVersion}
	if !validSiteCheckinConfigInput(in) {
		return siteCheckinConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteCheckinConfigSvc == nil {
		return siteCheckinConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.siteCheckinConfigSvc.Save(ctx, GetActor(ctx), r.IndexerId, in, r.Params.IdempotencyKey)
	if err != nil {
		return siteCheckinConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return siteCheckinConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	return SaveSiteCheckinConfig200JSONResponse{CommandKey: receipt.CommandKey, Config: siteCheckinConfigResponse(receipt.Config)}, nil
}

func (s *ServerHandler) ReadSiteCheckinConfigReceipt(ctx context.Context, r ReadSiteCheckinConfigReceiptRequestObject) (ReadSiteCheckinConfigReceiptResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return siteCheckinConfigProblem(ctx, err), nil
	}
	if r.IndexerId == uuid.Nil || !validDoubanSyncCommandKey(r.CommandKey) {
		return siteCheckinConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteCheckinConfigSvc == nil {
		return siteCheckinConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.siteCheckinConfigSvc.Receipt(ctx, GetActor(ctx), r.IndexerId, r.CommandKey)
	if err != nil {
		return siteCheckinConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return siteCheckinConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	return ReadSiteCheckinConfigReceipt200JSONResponse{CommandKey: receipt.CommandKey, Config: siteCheckinConfigResponse(receipt.Config)}, nil
}

func siteCheckinConfigResponse(c ports.SiteCheckinConfig) SiteCheckinConfig {
	return SiteCheckinConfig{IndexerId: c.IndexerID, ExpectedAccountId: c.ExpectedAccountID, Profile: SiteCheckinConfigProfile(c.Profile), Enabled: c.Enabled, Timezone: c.Timezone, DailyAt: c.DailyAt, Version: c.Version, SourceConfigVersion: c.SourceConfigVersion, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

type siteCheckinConfigErrorResponse struct{ problem ErrorResponse }

func siteCheckinConfigProblem(ctx context.Context, err error) siteCheckinConfigErrorResponse {
	status, code, message := 503, "SITE_CHECKIN_CONFIG_UNAVAILABLE", "site checkin configuration is temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "site checkin configuration access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = 400, "INVALID_REQUEST", "invalid site checkin configuration request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "source or configuration receipt not found"
	case errors.Is(err, ports.ErrEndpointVersionConflict):
		status, code, message = 409, "CONFLICT", "configuration version or command conflicts"
	}
	return siteCheckinConfigErrorResponse{problemResponse(status, code, message, GetRequestID(ctx))}
}
func (r siteCheckinConfigErrorResponse) VisitReadSiteCheckinConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r siteCheckinConfigErrorResponse) VisitSaveSiteCheckinConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r siteCheckinConfigErrorResponse) VisitReadSiteCheckinConfigReceiptResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
