package acquisition

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type EpisodeService struct {
	repo ports.EpisodeAcquisitionRepository
}

func NewEpisodeService(repo ports.EpisodeAcquisitionRepository) *EpisodeService {
	return &EpisodeService{repo: repo}
}
func (s *EpisodeService) AcquireReviewedEpisode(ctx context.Context, a *ports.Actor, c ports.AcquireReviewedEpisodeCommand, key string) (*ports.AcquireResult, error) {
	return s.repo.AcquireReviewedEpisode(ctx, a, c, key)
}
