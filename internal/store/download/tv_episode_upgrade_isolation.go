package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func (m *Manager) requireTVEpisodeUpgradeIsolation(ctx context.Context, op uuid.UUID) error {
	if e := postgres.RejectTVEpisodeUpgradeOperation(ctx, m.pool, op); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e == ports.ErrTVEpisodeUpgradeObservation {
			return e
		}
		return ports.ErrUnavailable
	}
	return nil
}