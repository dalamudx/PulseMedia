package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// ConfirmCompleteTVOrganization is an explicit user command, never a tracking
// callback. The repository resolves and validates all frozen evidence.
func (s *Service) ConfirmCompleteTVOrganization(ctx context.Context, actor *ports.Actor, refs ports.CompleteTVOrganizationReferences, key string) (*ports.CompleteTVOrganizationReceipt, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if refs.TaskID == uuid.Nil || refs.OperationID == uuid.Nil || refs.TrackingJobID == uuid.Nil || key == "" || len(key) > 120 {
		return nil, ports.ErrTransferReviewRequired
	}
	repo, ok := s.repo.(ports.CompleteTVOrganizationConfirmationRepository)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return repo.ConfirmCompleteTVOrganizationReferences(ctx, actor, refs, key)
}
