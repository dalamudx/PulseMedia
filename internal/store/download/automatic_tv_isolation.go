package download

import (
	"context"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Surviving automatic authority, preparation or demand evidence fences every
// generic send/observation path. Historical repository fixtures may predate the
// extension tables; the production App separately requires the current schema.
func (m *Manager) requireAutomaticTVIsolation(ctx context.Context, operation uuid.UUID) error {
	if m == nil || m.pool == nil || operation == uuid.Nil {
		return ports.ErrUnavailable
	}
	var authority, preparation bool
	if err := m.pool.QueryRow(ctx, `SELECT to_regclass('automatic_tv_execution_authority') IS NOT NULL, to_regclass('automatic_tv_submission_preparation') IS NOT NULL`).Scan(&authority, &preparation); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ports.ErrUnavailable
	}
	clauses := []string{`EXISTS(SELECT 1 FROM download_requirement WHERE operation_id=$1 AND submission_fence->>'kind'='automatic_tv_episode_v1')`}
	if authority {
		clauses = append(clauses, `EXISTS(SELECT 1 FROM automatic_tv_execution_authority WHERE operation_id=$1)`)
	}
	if preparation {
		clauses = append(clauses, `EXISTS(SELECT 1 FROM automatic_tv_submission_preparation WHERE operation_id=$1)`)
	}
	var automatic bool
	if err := m.pool.QueryRow(ctx, "SELECT "+strings.Join(clauses, " OR "), operation).Scan(&automatic); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ports.ErrUnavailable
	}
	if automatic {
		return ports.ErrAutomaticTVNeedsObservation
	}
	return nil
}
