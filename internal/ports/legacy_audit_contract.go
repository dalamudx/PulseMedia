package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type AuditEntry struct {
	ID         uuid.UUID      `json:"id"`
	ActorType  string         `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Details    map[string]any `json:"details"`
	CreatedAt  time.Time      `json:"created_at"`
}

// AuditWriter persists legacy standalone audit entries; it grants no business authority.
type AuditWriter interface {
	WriteAudit(ctx context.Context, entry AuditEntry) error
}
