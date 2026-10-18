package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type AcquireMovieCommand = ports.AcquireMovieCommand
type AcquireResult = ports.AcquireResult
type RoundSnapshot = ports.RoundSnapshot
type RoundDecision = ports.RoundDecision
type RoundJobAuthority = ports.RoundJobAuthority

var ErrUnsupportedCoverage = ports.ErrAcquisitionUnsupportedCoverage
var ErrOwnershipUncertain = ports.ErrAcquisitionOwnershipUncertain
var ErrAlreadyOwned = ports.ErrAcquisitionAlreadyOwned
var ErrRootNotOnline = ports.ErrAcquisitionRootNotOnline
var ErrLocalReuseRequired = ports.ErrAcquisitionLocalReuseRequired
var ErrUnitAlreadyReserved = ports.ErrAcquisitionUnitAlreadyReserved
var ErrRoundAborted = ports.ErrAcquisitionRoundAborted
var ErrRoundNotReady = ports.ErrAcquisitionRoundNotReady

type Service struct {
	repo         ports.AcquisitionRepository
	requirements ports.DownloadRequirementRepository
}

func NewService(repo ports.AcquisitionRepository) *Service { return &Service{repo: repo} }
func (s *Service) SetRequirementRepository(repo ports.DownloadRequirementRepository) {
	s.requirements = repo
}
func (s *Service) AcquireMovie(ctx context.Context, a *ports.Actor, c AcquireMovieCommand) (*AcquireResult, error) {
	return s.repo.AcquireMovie(ctx, a, c)
}
func (s *Service) FinalizeMovieRound(ctx context.Context, a *ports.Actor, id uuid.UUID, authority *RoundJobAuthority) (*RoundDecision, error) {
	return s.repo.FinalizeMovieRound(ctx, a, id, authority)
}
func (s *Service) PreSubmitRecheck(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.repo.PreSubmitRecheck(ctx, id)
}
func (s *Service) AttachRequirement(ctx context.Context, a *ports.Actor, op, unit, root uuid.UUID) error {
	if s.requirements == nil {
		return ports.ErrUnavailable
	}
	return s.requirements.AttachRequirement(ctx, a, op, unit, root)
}
func (s *Service) ReleaseRequirement(ctx context.Context, a *ports.Actor, op, unit, root uuid.UUID) (bool, error) {
	if s.requirements == nil {
		return false, ports.ErrUnavailable
	}
	return s.requirements.ReleaseRequirement(ctx, a, op, unit, root)
}
