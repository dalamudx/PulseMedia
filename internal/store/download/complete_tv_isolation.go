package download

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// requireGenericObservation excludes complete-set work before generic client
// calls, progress writes or organization. Any surviving immutable evidence is
// sufficient to fence the old path, even if another authority row is missing.
func (m *Manager) requireGenericObservation(ctx context.Context, operation uuid.UUID) error {
	if err := m.requireTVEpisodeUpgradeIsolation(ctx, operation); err != nil {
		return err
	}
	if err := m.requireAutomaticTVIsolation(ctx, operation); err != nil {
		return err
	}
	var dedicated bool
	err := m.pool.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM complete_tv_execution_authority WHERE operation_id=$1) OR
 EXISTS(SELECT 1 FROM complete_tv_submission_preparation WHERE operation_id=$1) OR
 EXISTS(SELECT 1 FROM complete_tv_submission_unit u JOIN download_task t ON t.id=u.task_id WHERE t.operation_id=$1)`, operation).Scan(&dedicated)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ports.ErrUnavailable
	}
	if dedicated {
		return ports.ErrCompleteTVNeedsObservation
	}
	return nil
}
