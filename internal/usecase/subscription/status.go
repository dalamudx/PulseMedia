package subscription

import (
	"context"
	domain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type StatusService struct {
	repo ports.SubscriptionStatusRepository
}

func NewStatusService(repo ports.SubscriptionStatusRepository) *StatusService {
	return &StatusService{repo}
}
func (s *StatusService) Read(ctx context.Context, a *ports.Actor, id uuid.UUID) (domain.StatusReport, error) {
	if a == nil {
		return domain.StatusReport{}, ports.ErrUnauthorized
	}
	snap, err := s.repo.ReadSubscriptionStatus(ctx, a, id)
	if err != nil {
		return domain.StatusReport{}, err
	}
	return domain.DeriveStatus(snap), nil
}
