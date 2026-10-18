package library

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type ScanScheduler struct {
	repo ports.LibraryScanSchedulingRepository
}

func NewScanScheduler(repo ports.LibraryScanSchedulingRepository) *ScanScheduler {
	return &ScanScheduler{repo: repo}
}
func (s *ScanScheduler) QueueDue(ctx context.Context, limit int32) (int, error) {
	return s.repo.QueueDue(ctx, limit)
}
