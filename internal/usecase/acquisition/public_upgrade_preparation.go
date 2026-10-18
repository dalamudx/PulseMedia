package acquisition

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"time"
)

type PublicUpgradePreparationService struct {
	repo      ports.PublicMovieUpgradePreparationRepository
	inspector ports.MovieUpgradePreparationInspector
	ids       ports.IDGen
}

func NewPublicUpgradePreparationService(r ports.PublicMovieUpgradePreparationRepository, i ports.MovieUpgradePreparationInspector, ids ports.IDGen) (*PublicUpgradePreparationService, error) {
	if r == nil || i == nil || ids == nil {
		return nil, ports.ErrUnavailable
	}
	return &PublicUpgradePreparationService{r, i, ids}, nil
}
func (s *PublicUpgradePreparationService) Prepare(ctx context.Context, a *ports.Actor, c ports.PublicMovieUpgradePreparation, key string) (*ports.MovieUpgradePreparationResult, error) {
	if err := upgradeReservationActor(a); err != nil {
		return nil, err
	}
	if c.SubscriptionID == uuid.Nil || c.OperationID == uuid.Nil || !c.Confirmed || key == "" || strings.TrimSpace(key) != key || len(key) > 130 {
		return nil, ports.ErrUpgradeReservationConflict
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	old, err := s.repo.ReadMovieUpgradePreparation(work, a, c, key)
	if err != nil || old != nil {
		return old, err
	}
	owner := s.ids.NewUUID()
	if owner == uuid.Nil {
		return nil, ports.ErrUnavailable
	}
	lease, err := s.repo.ClaimMovieUpgradePreparation(work, a, ports.ClaimMovieUpgradePreparation{OperationID: c.OperationID, Owner: owner})
	if err != nil {
		// A concurrent identical command may have committed between lookup and claim.
		// This is a read-only recovery, never a claim/fetch retry.
		if errors.Is(err, ports.ErrUpgradeReservationConflict) {
			if old, e := s.repo.ReadMovieUpgradePreparation(work, a, c, key); e != nil || old != nil {
				return old, e
			}
		}
		return nil, err
	}
	if lease == nil || lease.OperationID != c.OperationID || lease.Owner != owner || lease.Token == uuid.Nil {
		return nil, ports.ErrUnavailable
	}
	source, err := s.repo.LoadMovieUpgradePreparationWork(work, a, c, *lease)
	if err != nil {
		return nil, err
	}
	proof, err := s.inspector.InspectMovieUpgradePreparation(work, source)
	if err != nil {
		return nil, err
	}
	result, err := s.repo.PrepareMovieUpgrade(work, a, ports.PrepareMovieUpgrade{OperationID: c.OperationID, Owner: lease.Owner, Token: lease.Token, Proof: proof}, key)
	if err != nil {
		return nil, err
	}
	if result == nil || result.OperationID != c.OperationID || result.TaskID == uuid.Nil || result.Status != "prepared_not_sent" || result.ExecutionAllowed {
		return nil, ports.ErrUnavailable
	}
	return result, nil
}
