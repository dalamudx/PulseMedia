package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Re-check time after all audit/event SQL; it never applies to observation of an already-sent physical outcome.
func GuardEpisodeNumberingTime(ctx context.Context, q *db.Queries, op uuid.UUID) error {
	r, e := q.GetEpisodeExecutionAuthority(ctx, op)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return ports.ErrUnavailable
	}
	var f ports.EpisodeExecutionSnapshot
	if json.Unmarshal(r.Snapshot, &f) != nil || !f.ValidNumberingShape() {
		return ports.ErrDownloadDemandChanged
	}
	if f.MappingValidUntil == nil {
		return nil
	}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return ports.ErrUnavailable
	}
	if !f.MappingValidUntil.After(now.Time) {
		return ports.ErrDownloadDemandChanged
	}
	return nil
}
