package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type UpgradeObservationService struct {
	repo ports.UpgradeObservationRepository
}

func NewUpgradeObservationService(r ports.UpgradeObservationRepository) *UpgradeObservationService {
	return &UpgradeObservationService{r}
}
func (s *UpgradeObservationService) Observe(ctx context.Context, a *ports.Actor, r ports.UpgradePreviewRequest) (*ports.FrozenUpgradeObservation, error) {
	if e := upgradeReservationActor(a); e != nil {
		return nil, e
	}
	return s.repo.ReadFrozenUpgradeObservation(ctx, a, r)
}
