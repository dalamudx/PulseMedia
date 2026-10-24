package ports

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
	"github.com/google/uuid"
)

// AuditSummary deliberately excludes the historical arbitrary JSON payload.
type AuditSummary struct {
	ID         uuid.UUID
	ActorType  string
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	CreatedAt  time.Time
}

type AuditTimelineRequest struct {
	Filter auditlog.Filter
	Limit  int
	Cursor string
}

type AuditTimelinePage struct {
	Items      []AuditSummary
	NextCursor string
}

// AuditTimelineReader rechecks the current administrator before reading the
// existing transactional audit ledger. It never writes another audit entry.
type AuditTimelineReader interface {
	ReadAuditTimeline(context.Context, *Actor, auditlog.Filter, *auditlog.Position, int) ([]AuditSummary, error)
}
