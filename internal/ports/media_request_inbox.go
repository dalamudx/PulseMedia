package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type MediaRequestInboxSummary struct {
	PendingCount int64
	ObservedAt   time.Time
}

type MediaRequestInboxReader interface {
	ReadMediaRequestInbox(context.Context, *Actor) (*MediaRequestInboxSummary, error)
	ListPendingMediaRequests(context.Context, *Actor, int, *uuid.UUID) ([]MediaRequest, error)
}
