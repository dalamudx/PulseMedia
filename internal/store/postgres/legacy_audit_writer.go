package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type LegacyAuditWriter struct{ pool *pgxpool.Pool }

func NewLegacyAuditWriter(pool *pgxpool.Pool) *LegacyAuditWriter {
	return &LegacyAuditWriter{pool: pool}
}

var _ ports.AuditWriter = (*LegacyAuditWriter)(nil)

func (w *LegacyAuditWriter) WriteAudit(ctx context.Context, entry ports.AuditEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(entry.Details)
	if err != nil || entry.Details == nil {
		payload = []byte("{}")
	}
	// Existing sqlc InsertAuditLog uses statement_timestamp(); this helper preserves the injected clock.
	_, err = w.pool.Exec(ctx, `INSERT INTO audit_log (id, actor_type, actor_id, action, target_type, target_id, payload, created_at)
 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, entry.ID, entry.ActorType, entry.ActorID, entry.Action, entry.TargetType, entry.TargetID, payload, entry.CreatedAt)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ports.ErrUnavailable
	}
	return nil
}
