package search

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// AutomaticRoundCoordinator routes only an actual non-TV target to the movie
// coordinator. Failed TV prerequisites remain failures of the original target.
type AutomaticRoundCoordinator struct {
	movie ports.ScheduledRoundCoordinator
	tv    ports.AutomaticTVRoundRepository
}

func NewAutomaticRoundCoordinator(movie ports.ScheduledRoundCoordinator, tv ports.AutomaticTVRoundRepository) *AutomaticRoundCoordinator {
	return &AutomaticRoundCoordinator{movie: movie, tv: tv}
}

var _ ports.ScheduledRoundCoordinator = (*AutomaticRoundCoordinator)(nil)

func (c *AutomaticRoundCoordinator) ScheduleAutomaticRound(ctx context.Context, wanted uuid.UUID, window time.Duration) error {
	if c.tv == nil {
		return ports.ErrUnavailable
	}
	_, err := c.tv.StartAutomaticTVRound(ctx, ports.AutomaticTVRoundStart{WantedID: wanted, Window: window})
	if !errors.Is(err, ports.ErrAutomaticTVNotApplicable) {
		return err
	}
	if c.movie == nil {
		return ports.ErrUnavailable
	}
	return c.movie.ScheduleAutomaticRound(ctx, wanted, window)
}

// Shared acquisition and legacy finalize jobs retain their original movie
// authority. Automatic TV finalize jobs have their own kind and executor.
func (c *AutomaticRoundCoordinator) ScheduleSharedRound(ctx context.Context, wanted uuid.UUID) error {
	if c.movie == nil {
		return ports.ErrUnavailable
	}
	return c.movie.ScheduleSharedRound(ctx, wanted)
}

func (c *AutomaticRoundCoordinator) FinalizeScheduledRound(ctx context.Context, id uuid.UUID, authority ports.RoundJobAuthority) error {
	if c.movie == nil {
		return ports.ErrUnavailable
	}
	return c.movie.FinalizeScheduledRound(ctx, id, authority)
}
