package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type PublicUpgradeRetirementService struct {
	repo ports.MovieUpgradeRetirementRepository
}

func NewPublicUpgradeRetirementService(r ports.MovieUpgradeRetirementRepository) (*PublicUpgradeRetirementService, error) {
	if r == nil {
		return nil, ports.ErrUnavailable
	}
	return &PublicUpgradeRetirementService{repo: r}, nil
}
func (s *PublicUpgradeRetirementService) Preview(ctx context.Context, a *ports.Actor, sub, op uuid.UUID) (*ports.MovieUpgradeRetirementPreview, error) {
	if e := publicUpgradeOrganizationActor(a, sub, op); e != nil {
		return nil, e
	}
	return s.repo.ReadMovieUpgradeRetirementPreview(ctx, a, sub, op)
}
func (s *PublicUpgradeRetirementService) Confirm(ctx context.Context, a *ports.Actor, sub uuid.UUID, c ports.ConfirmMovieUpgradeRetirement, key string) (*ports.MovieUpgradeRetirementReceipt, error) {
	if e := publicUpgradeOrganizationActor(a, sub, c.OperationID); e != nil {
		return nil, e
	}
	if !c.Confirmed || len(c.InputStamp) != 64 || !publicUpgradeOrganizationKey(key) {
		return nil, ports.ErrUpgradeReservationConflict
	}
	return s.repo.ConfirmMovieUpgradeRetirement(ctx, a, sub, key, c)
}
