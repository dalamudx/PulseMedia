package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// A published file with an uncommitted outcome still belongs to its explicit review.
func guardExternalTransferTarget(ctx context.Context, q *db.Queries, rootID uuid.UUID, relativePath string) error {
	ready, err := q.ExternalTransferTargetSchemaReady(ctx)
	if err != nil {
		return untrackedError(ctx, err)
	}
	if !ready {
		return nil
	}
	blocked, err := q.ExternalTransferTargetBlocked(ctx, db.ExternalTransferTargetBlockedParams{RootID: rootID, RelativePath: relativePath})
	if err != nil {
		return untrackedError(ctx, err)
	}
	if blocked {
		return ports.ErrMovieLinkStale
	}
	return nil
}
