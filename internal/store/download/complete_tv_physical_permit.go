package download

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

// A durable permit is never reclaimed by timeout or by session disappearance.
// Only the synchronous owner removes its token after real I/O and commit return.
// An abandoned permit requires explicit manual review, never blind replay.
func (m *Manager) acquireCompleteTVPhysicalPermit(ctx context.Context, operation uuid.UUID, pid uint32, key int64) (func(), error) {
	token := m.idgen.NewUUID()
	var acquired uuid.UUID
	err := m.pool.QueryRow(ctx, `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key) VALUES($1,$2,$3,$4) ON CONFLICT(operation_id) DO NOTHING RETURNING token`, operation, token, int32(pid), key).Scan(&acquired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrCompleteTVNeedsObservation
	}
	if err != nil {
		return nil, err
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Failed cleanup deliberately retains protection for manual observation.
		_, _ = m.pool.Exec(cleanup, `DELETE FROM complete_tv_physical_permit WHERE operation_id=$1 AND token=$2`, operation, acquired)
	}, nil
}
