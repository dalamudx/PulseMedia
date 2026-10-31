package librarymaintenance

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// The repository retains short UoWs and capability checks; this service owns
// physical action selection, observation, writeback and final release ordering.
type Service struct {
	ports.LibraryMaintenanceRepository
}

func NewService(repo ports.LibraryMaintenanceRepository) *Service { return &Service{repo} }

func (s *Service) Command(ctx context.Context, a *ports.Actor, c ports.LibraryMaintenanceCommand, key string) (*ports.LibraryMaintenanceResult, error) {
	prepared, err := s.PrepareCommand(ctx, a, c, key)
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, ports.ErrUnavailable
	}
	defer prepared.Close()
	if previous := prepared.PreviousResult(); previous != nil {
		return previous, nil
	}
	err = prepared.BeforeCall(ctx)
	if err == nil {
		if c.Action == "cleanup" {
			err = prepared.Remove(ctx)
		} else {
			err = prepared.Rename(ctx)
		}
	}
	trusted := err == nil && ctx.Err() == nil
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if trusted {
		trusted = prepared.Observe(record) == nil
	}
	if trusted {
		out, err := prepared.Complete(record, true)
		if err == nil {
			return out, nil
		}
	}
	// A failed final transaction cannot authorize another physical attempt.
	return prepared.Complete(record, false)
}

var _ ports.LibraryMaintenanceRepository = (*Service)(nil)
