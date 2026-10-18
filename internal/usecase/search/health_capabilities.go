package search

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"time"
)

// Both health requests share the physical permit and reserve the durable budget separately.
func (s *Service) reserveHealthSearch(ctx context.Context, permit ports.SearchExecutionPermit) error {
	if s.minimumInterval > 0 {
		timer := time.NewTimer(s.minimumInterval)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return permit.ReserveHealthSearch(ctx)
}
