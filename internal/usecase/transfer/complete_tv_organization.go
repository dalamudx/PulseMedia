package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type CompleteTVOrganizationService struct {
	repo ports.CompleteTVOrganizationRepository
}

func NewCompleteTVOrganizationService(repo ports.CompleteTVOrganizationRepository) *CompleteTVOrganizationService {
	return &CompleteTVOrganizationService{repo: repo}
}
func (s *CompleteTVOrganizationService) Confirm(ctx context.Context, actor *ports.Actor, c ports.CompleteTVOrganizationCommand, key string) (*ports.CompleteTVOrganizationReceipt, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if !c.Confirmed || c.TaskID == [16]byte{} || c.OperationID == [16]byte{} || c.TrackingJobID == [16]byte{} || len(c.ExpectedEvidenceStamp) != 64 || key == "" || len(key) > 120 {
		return nil, ports.ErrTransferReviewRequired
	}
	return s.repo.ConfirmCompleteTVOrganization(ctx, actor, c, key)
}
