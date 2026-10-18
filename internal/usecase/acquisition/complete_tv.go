package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// CompleteTVService delegates whole-set acquisition to its transactional repository.
type CompleteTVService struct {
	repo ports.CompleteTVCoverageAcquisitionRepository
}

func NewCompleteTVService(repo ports.CompleteTVCoverageAcquisitionRepository) *CompleteTVService {
	return &CompleteTVService{repo: repo}
}
func (s *CompleteTVService) Acquire(ctx context.Context, actor *ports.Actor, command ports.AcquireCompleteTVCoverageCommand, key string) (*ports.AcquireResult, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Role != ports.RoleAdmin || actor.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	return s.repo.AcquireCompleteTVCoverage(ctx, actor, command, key)
}
