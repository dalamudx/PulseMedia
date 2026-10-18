package notify

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type BatchQueryService struct {
	repo ports.NotificationBatchQueryRepository
}

func NewBatchQueryService(repo ports.NotificationBatchQueryRepository) *BatchQueryService {
	return &BatchQueryService{repo}
}
func (s *BatchQueryService) List(ctx context.Context, a *ports.Actor) ([]ports.NotificationBatchSummary, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	return s.repo.ListNotificationBatches(ctx, a)
}
