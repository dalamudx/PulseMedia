package httpapi

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"net/http"
)

func (s *ServerHandler) SetPublicUpgradeRetirementService(service *acquisition.PublicUpgradeRetirementService) {
	s.upgradeRetirementSvc = service
}
func (r upgradeOrganizationResponse) VisitPreviewSubscriptionUpgradeRetirementResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, r.status, r.body)
}
func (r upgradeOrganizationResponse) VisitConfirmSubscriptionUpgradeRetirementResponse(w http.ResponseWriter) error {
	return writeUpgradeCommandJSON(w, r.status, r.body)
}
func (s *ServerHandler) PreviewSubscriptionUpgradeRetirement(ctx context.Context, r PreviewSubscriptionUpgradeRetirementRequestObject) (PreviewSubscriptionUpgradeRetirementResponseObject, error) {
	if r.Id == uuid.Nil || r.Body == nil || r.Body.OperationId == uuid.Nil {
		return upgradeOrganizationInvalid(ctx), nil
	}
	if s.upgradeRetirementSvc == nil {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.upgradeRetirementSvc.Preview(ctx, GetActor(ctx), r.Id, r.Body.OperationId)
	if e != nil {
		return upgradeOrganizationFailed(ctx, e), nil
	}
	if out == nil || out.OperationID != r.Body.OperationId || out.OldFileID == uuid.Nil || out.NewFileID == uuid.Nil || out.OldFileID == out.NewFileID || !validUpgradeDigest(out.InputStamp) || out.RetentionDays != 7 || out.ExecutionAllowed {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	return upgradeOrganizationResponse{http.StatusOK, out}, nil
}
func (s *ServerHandler) ConfirmSubscriptionUpgradeRetirement(ctx context.Context, r ConfirmSubscriptionUpgradeRetirementRequestObject) (ConfirmSubscriptionUpgradeRetirementResponseObject, error) {
	if r.Id == uuid.Nil || r.Body == nil || r.Body.OperationId == uuid.Nil || !bool(r.Body.Confirmed) || !validUpgradeDigest(r.Body.InputStamp) {
		return upgradeOrganizationInvalid(ctx), nil
	}
	if s.upgradeRetirementSvc == nil {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.upgradeRetirementSvc.Confirm(ctx, GetActor(ctx), r.Id, ports.ConfirmMovieUpgradeRetirement{OperationID: r.Body.OperationId, InputStamp: r.Body.InputStamp, Confirmed: true}, r.Params.IdempotencyKey)
	if e != nil {
		return upgradeOrganizationFailed(ctx, e), nil
	}
	if out == nil || out.OperationID != r.Body.OperationId || out.CommandID == uuid.Nil || out.QueuedAt.IsZero() || out.Status != "retirement_queued" || out.ExecutionAllowed {
		return upgradeOrganizationFailed(ctx, ports.ErrUnavailable), nil
	}
	return upgradeOrganizationResponse{http.StatusAccepted, out}, nil
}
