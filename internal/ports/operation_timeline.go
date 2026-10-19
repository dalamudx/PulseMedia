package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// This projection never grants approval, recovery or filesystem authority.
type OperationTimelineRequest struct {
	Limit  int
	Cursor string
}

type OperationTimelineEntry struct {
	StepID     uuid.UUID
	Order      int32
	Action     string
	State      string
	FileName   string
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

type OperationTimelinePage struct {
	OperationID uuid.UUID
	PlanID      *uuid.UUID
	PlanStatus  *string
	Items       []OperationTimelineEntry
	NextCursor  string
	ObservedAt  time.Time
}

type OperationTimelineReader interface {
	ReadOperationTimeline(context.Context, *Actor, uuid.UUID, OperationTimelineRequest) (*OperationTimelinePage, error)
}
