package httpapi

import (
	"context"
	"errors"
	rules "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"net/http"
)

type seedingConfigApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID) (*ports.SeedingConfigView, error)
	Save(context.Context, *ports.Actor, uuid.UUID, ports.SeedingConfigInput, string) (*ports.SeedingConfigReceipt, error)
	Receipt(context.Context, *ports.Actor, uuid.UUID, string) (*ports.SeedingConfigReceipt, error)
}

func (s *ServerHandler) SetSeedingConfigService(service seedingConfigApplication) {
	s.seedingConfigSvc = service
}
func seedingRuleInput(r SeedingRule) rules.Rule {
	return rules.Rule{Mode: rules.MatchMode(r.Mode), DeleteFiles: bool(r.DeleteFiles), MinSeedingSeconds: r.MinSeedingSeconds, MinShareRatio: r.MinShareRatio, LowFreeBytes: r.LowFreeBytes}
}
func seedingConfigResponse(c ports.SeedingConfigView) SeedingConfig {
	return SeedingConfig{ClientId: c.ClientID, OwnerId: c.OwnerID, Version: c.Version, ClientUpdatedAt: c.ClientUpdatedAt, Enabled: c.Enabled, IntervalSeconds: c.IntervalSeconds, UpdatedAt: c.UpdatedAt, Rule: SeedingRule{Mode: SeedingRuleMode(c.Rule.Mode), DeleteFiles: SeedingRuleDeleteFiles(c.Rule.DeleteFiles), MinSeedingSeconds: c.Rule.MinSeedingSeconds, MinShareRatio: c.Rule.MinShareRatio, LowFreeBytes: c.Rule.LowFreeBytes}}
}
func seedingConfigReceiptResponse(r *ports.SeedingConfigReceipt) SeedingConfigReceipt {
	return SeedingConfigReceipt{CommandKey: r.CommandKey, Config: seedingConfigResponse(r.Config)}
}
func (s *ServerHandler) ReadSeedingConfig(ctx context.Context, r ReadSeedingConfigRequestObject) (ReadSeedingConfigResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return seedingConfigProblem(ctx, err), nil
	}
	if r.Id == uuid.Nil {
		return seedingConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.seedingConfigSvc == nil {
		return seedingConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	record, err := s.seedingConfigSvc.Read(ctx, GetActor(ctx), r.Id)
	if err != nil {
		return seedingConfigProblem(ctx, err), nil
	}
	var config *SeedingConfig
	if record != nil {
		v := seedingConfigResponse(*record)
		config = &v
	}
	return ReadSeedingConfig200JSONResponse{Config: config}, nil
}
func (s *ServerHandler) SaveSeedingConfig(ctx context.Context, r SaveSeedingConfigRequestObject) (SaveSeedingConfigResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return seedingConfigProblem(ctx, err), nil
	}
	if r.Id == uuid.Nil || r.Body == nil || !validCookieCloudCommandKey(r.Params.IdempotencyKey) {
		return seedingConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.seedingConfigSvc == nil {
		return seedingConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	in := ports.SeedingConfigInput{ExpectedVersion: r.Body.ExpectedVersion, ExpectedClientUpdatedAt: r.Body.ExpectedClientUpdatedAt, Enabled: r.Body.Enabled, IntervalSeconds: r.Body.IntervalSeconds, Rule: seedingRuleInput(r.Body.Rule)}
	receipt, err := s.seedingConfigSvc.Save(ctx, GetActor(ctx), r.Id, in, r.Params.IdempotencyKey)
	if err != nil {
		return seedingConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return seedingConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	return SaveSeedingConfig200JSONResponse(seedingConfigReceiptResponse(receipt)), nil
}
func (s *ServerHandler) ReadSeedingConfigReceipt(ctx context.Context, r ReadSeedingConfigReceiptRequestObject) (ReadSeedingConfigReceiptResponseObject, error) {
	if err := siteCheckinConfigActorError(GetActor(ctx)); err != nil {
		return seedingConfigProblem(ctx, err), nil
	}
	if r.Id == uuid.Nil || !validCookieCloudCommandKey(r.CommandKey) {
		return seedingConfigProblem(ctx, ports.ErrRejected), nil
	}
	if s.seedingConfigSvc == nil {
		return seedingConfigProblem(ctx, ports.ErrUnavailable), nil
	}
	receipt, err := s.seedingConfigSvc.Receipt(ctx, GetActor(ctx), r.Id, r.CommandKey)
	if err != nil {
		return seedingConfigProblem(ctx, err), nil
	}
	if receipt == nil {
		return seedingConfigProblem(ctx, ports.ErrNotFound), nil
	}
	return ReadSeedingConfigReceipt200JSONResponse(seedingConfigReceiptResponse(receipt)), nil
}

type seedingConfigErrorResponse struct{ problem ErrorResponse }

func seedingConfigProblem(ctx context.Context, err error) seedingConfigErrorResponse {
	status, code, message := http.StatusServiceUnavailable, "UNAVAILABLE", "seeding configuration temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = http.StatusForbidden, "FORBIDDEN", "seeding configuration access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", "invalid seeding configuration request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "downloader or configuration receipt not found"
	case errors.Is(err, ports.ErrSeedingConfigConflict):
		status, code, message = http.StatusConflict, "CONFLICT", "configuration version or original command conflicts"
	}
	return seedingConfigErrorResponse{problem: problemResponse(status, code, message, GetRequestID(ctx))}
}
func (r seedingConfigErrorResponse) VisitReadSeedingConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r seedingConfigErrorResponse) VisitSaveSeedingConfigResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
func (r seedingConfigErrorResponse) VisitReadSeedingConfigReceiptResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
