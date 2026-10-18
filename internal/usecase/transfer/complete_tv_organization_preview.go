package transfer

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type CompleteTVOrganizationPreviewService struct {
	repo ports.CompleteTVOrganizationPreviewRepository
}

func NewCompleteTVOrganizationPreviewService(repo ports.CompleteTVOrganizationPreviewRepository) *CompleteTVOrganizationPreviewService {
	return &CompleteTVOrganizationPreviewService{repo: repo}
}
func (s *CompleteTVOrganizationPreviewService) Read(ctx context.Context, a *ports.Actor, op uuid.UUID) (*ports.CompleteTVOrganizationPreviewPage, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if op == uuid.Nil {
		return nil, ports.ErrTransferReviewRequired
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.ReadCompleteTVOrganizations(ctx, a, op)
}
