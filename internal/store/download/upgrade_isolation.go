package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Called at each public generic entry before task/receipt/state shortcuts.
// Any surviving goal=upgrade fence is sufficient; no acquire path may convert
// this reservation into physical work, including reconciliation/observation.
func (m *Manager) requireNoFrozenUpgrade(ctx context.Context, operation uuid.UUID) error {
	var frozen bool
	e := m.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_authority WHERE operation_id=$1) OR EXISTS(SELECT 1 FROM download_requirement WHERE operation_id=$1 AND submission_fence->>'goal'='upgrade') OR EXISTS(SELECT 1 FROM unit_reservation WHERE operation_id=$1 AND purpose='upgrade')`, operation).Scan(&frozen)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ports.ErrUnavailable
	}
	if frozen {
		return ports.ErrUpgradeNotExecutable
	}
	return nil
}
