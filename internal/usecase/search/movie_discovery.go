package search

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type MovieDiscoveryService struct {
	repo ports.MovieDiscoveryRepository
}

func NewMovieDiscoveryService(repo ports.MovieDiscoveryRepository) *MovieDiscoveryService {
	return &MovieDiscoveryService{repo: repo}
}

// RunBatch uses the App-owned ticker; no goroutine or network I/O starts here.
func (s *MovieDiscoveryService) RunBatch(ctx context.Context, limit int32) (int, error) {
	if s == nil || s.repo == nil {
		return 0, ports.ErrUnavailable
	}
	if limit < 1 || limit > 20 {
		limit = 20
	}
	ids, err := s.repo.ListDueMovieDiscoveries(ctx, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		scheduled, err := s.repo.ScheduleMovieDiscovery(ctx, id)
		if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrRejected) || errors.Is(err, ports.ErrNotFound) {
			continue
		}
		if err != nil {
			return count, err
		}
		if scheduled {
			count++
		}
	}
	return count, nil
}
func (s *MovieDiscoveryService) Read(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.MovieDiscoveryStatus, error) {
	if err := authorizeInteractiveSearch(a); err != nil {
		return nil, err
	}
	if a.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.ReadMovieDiscovery(ctx, a, id)
}
