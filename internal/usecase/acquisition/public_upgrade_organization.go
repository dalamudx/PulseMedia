package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
)

type PublicUpgradeOrganizationService struct {
	repo ports.PublicMovieUpgradeOrganizationRepository
}

func NewPublicUpgradeOrganizationService(r ports.PublicMovieUpgradeOrganizationRepository) (*PublicUpgradeOrganizationService, error) {
	if r == nil {
		return nil, ports.ErrUnavailable
	}
	return &PublicUpgradeOrganizationService{repo: r}, nil
}
func publicUpgradeOrganizationActor(a *ports.Actor, sub, op uuid.UUID) error {
	if e := upgradeReservationActor(a); e != nil {
		return e
	}
	if sub == uuid.Nil || op == uuid.Nil {
		return ports.ErrUpgradeReservationConflict
	}
	return nil
}
func publicUpgradeOrganizationKey(k string) bool {
	return k != "" && len(k) <= 100 && strings.TrimSpace(k) == k && !strings.ContainsAny(k, ",\r\n\t")
}
func (s *PublicUpgradeOrganizationService) Review(ctx context.Context, a *ports.Actor, sub, op uuid.UUID) (*ports.MovieUpgradeOrganizationReview, error) {
	if e := publicUpgradeOrganizationActor(a, sub, op); e != nil {
		return nil, e
	}
	return s.repo.ReviewMovieUpgradeOrganization(ctx, a, sub, op)
}
func (s *PublicUpgradeOrganizationService) State(ctx context.Context, a *ports.Actor, sub, op uuid.UUID) (*ports.MovieUpgradeOrganizationState, error) {
	if e := publicUpgradeOrganizationActor(a, sub, op); e != nil {
		return nil, e
	}
	return s.repo.ReadMovieUpgradeOrganizationState(ctx, a, sub, op)
}
func (s *PublicUpgradeOrganizationService) Authorize(ctx context.Context, a *ports.Actor, sub uuid.UUID, c ports.AuthorizeMovieUpgradeOrganization, key string) (*ports.MovieUpgradeOrganizationAuthorization, error) {
	if e := publicUpgradeOrganizationActor(a, sub, c.OperationID); e != nil {
		return nil, e
	}
	if c.ObservationID == uuid.Nil || c.InputStamp == "" || !c.Confirmed || !publicUpgradeOrganizationKey(key) {
		return nil, ports.ErrUpgradeReservationConflict
	}
	return s.repo.AuthorizeMovieUpgradeOrganization(ctx, a, sub, c, key)
}
func (s *PublicUpgradeOrganizationService) Prepare(ctx context.Context, a *ports.Actor, sub uuid.UUID, c ports.PrepareMovieUpgradeOrganization, key string) (*ports.MovieUpgradeOrganizationPreparation, error) {
	if e := publicUpgradeOrganizationActor(a, sub, c.OperationID); e != nil {
		return nil, e
	}
	if c.AuthorizationID == uuid.Nil || !c.Confirmed || !publicUpgradeOrganizationKey(key) {
		return nil, ports.ErrUpgradeReservationConflict
	}
	return s.repo.PrepareMovieUpgradeOrganization(ctx, a, sub, c, key)
}
func (s *PublicUpgradeOrganizationService) Queue(ctx context.Context, a *ports.Actor, sub uuid.UUID, c ports.QueueMovieUpgradeOrganization, key string) (*ports.MovieUpgradeOrganizationJobReceipt, error) {
	if e := publicUpgradeOrganizationActor(a, sub, c.OperationID); e != nil {
		return nil, e
	}
	if c.PreparationID == uuid.Nil || !c.Confirmed || !publicUpgradeOrganizationKey(key) {
		return nil, ports.ErrUpgradeReservationConflict
	}
	return s.repo.QueueMovieUpgradeOrganization(ctx, a, sub, c, key)
}
