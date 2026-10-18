package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"net/http"
)

func (s *ServerHandler) SetPublicUpgradeSendService(service *acquisition.PublicUpgradeSendService) {
	s.upgradeSendSvc = service
}

type upgradeSendFailure upgradeCommandFailure

func (e upgradeSendFailure) VisitConfirmSubscriptionUpgradeSendResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, e.status, e.body)
}

type upgradeSendSuccess struct {
	result *ports.MovieUpgradeSendJobReceipt
}

func (e upgradeSendSuccess) VisitConfirmSubscriptionUpgradeSendResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, http.StatusAccepted, e.result)
}
func (s *ServerHandler) ConfirmSubscriptionUpgradeSend(ctx context.Context, r ConfirmSubscriptionUpgradeSendRequestObject) (ConfirmSubscriptionUpgradeSendResponseObject, error) {
	if r.Body == nil || r.Id == uuid.Nil || r.Body.OperationId == uuid.Nil || r.Body.TaskId == uuid.Nil || !bool(r.Body.Confirmed) {
		return upgradeSendFailure(invalidUpgradeCommand(ctx)), nil
	}
	if s.upgradeSendSvc == nil {
		return upgradeSendFailure(failedUpgradeCommand(ctx, ports.ErrUnavailable)), nil
	}
	result, err := s.upgradeSendSvc.Confirm(ctx, GetActor(ctx), ports.QueueMovieUpgradeSend{SubscriptionID: r.Id, OperationID: r.Body.OperationId, TaskID: r.Body.TaskId, Confirmed: bool(r.Body.Confirmed)}, r.Params.IdempotencyKey)
	if err != nil {
		return upgradeSendFailure(failedUpgradeCommand(ctx, err)), nil
	}
	if result == nil || result.OperationID != r.Body.OperationId || result.TaskID != r.Body.TaskId || result.JobID == uuid.Nil || result.CommandID == uuid.Nil || result.QueuedAt.IsZero() || result.Status != "queued" || result.ExecutionAllowed {
		return upgradeSendFailure(failedUpgradeCommand(ctx, ports.ErrUnavailable)), nil
	}
	return upgradeSendSuccess{result}, nil
}
