package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type UpgradeReservationService struct {
	repo ports.UpgradeReservationRepository
}

func NewUpgradeReservationService(r ports.UpgradeReservationRepository) *UpgradeReservationService {
	return &UpgradeReservationService{r}
}
func upgradeReservationActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return ports.ErrForbidden
	}
	return nil
}
func (s *UpgradeReservationService) SetMode(ctx context.Context, a *ports.Actor, c ports.SetUpgradeModeCommand, key string) (*ports.UpgradeModeResult, error) {
	if e := upgradeReservationActor(a); e != nil {
		return nil, e
	}
	return s.repo.SetUpgradeMode(ctx, a, c, key)
}
func (s *UpgradeReservationService) Reserve(ctx context.Context, a *ports.Actor, c ports.ReserveUpgradeCommand, key string) (*ports.UpgradeReservationResult, error) {
	if e := upgradeReservationActor(a); e != nil {
		return nil, e
	}
	return s.repo.ReserveUpgrade(ctx, a, c, key)
}
