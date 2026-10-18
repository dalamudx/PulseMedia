package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"net/http"
)

func (s *ServerHandler) SetPublicUpgradePreparationService(service *acquisition.PublicUpgradePreparationService) {
	s.upgradePreparationSvc = service
}

type upgradePreparationFailure upgradeCommandFailure

func (e upgradePreparationFailure) VisitPrepareSubscriptionUpgradeResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, e.status, e.body)
}

type upgradePreparationSuccess struct {
	result *ports.MovieUpgradePreparationResult
}

func (e upgradePreparationSuccess) VisitPrepareSubscriptionUpgradeResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, http.StatusAccepted, e.result)
}
func (s *ServerHandler) PrepareSubscriptionUpgrade(ctx context.Context, r PrepareSubscriptionUpgradeRequestObject) (PrepareSubscriptionUpgradeResponseObject, error) {
	if r.Body == nil || r.Id == uuid.Nil || r.Body.OperationId == uuid.Nil || !bool(r.Body.Confirmed) {
		return upgradePreparationFailure(invalidUpgradeCommand(ctx)), nil
	}
	if s.upgradePreparationSvc == nil {
		return upgradePreparationFailure(failedUpgradeCommand(ctx, ports.ErrUnavailable)), nil
	}
	result, err := s.upgradePreparationSvc.Prepare(ctx, GetActor(ctx), ports.PublicMovieUpgradePreparation{SubscriptionID: r.Id, OperationID: r.Body.OperationId, Confirmed: bool(r.Body.Confirmed)}, r.Params.IdempotencyKey)
	if err != nil {
		return upgradePreparationFailure(failedUpgradeCommand(ctx, err)), nil
	}
	if result == nil || result.Status != "prepared_not_sent" || result.ExecutionAllowed || !validUpgradeDigest(result.RawSHA256) {
		return upgradePreparationFailure(failedUpgradeCommand(ctx, ports.ErrUnavailable)), nil
	}
	return upgradePreparationSuccess{result}, nil
}
