package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

type OperationSnapshot struct {
	OperationID    uuid.UUID
	Status         string
	DownloadStatus *string
	Progress       *float32
	PlanStatus     *string
	PlanID         *uuid.UUID
}

type OperationListRequest struct {
	View   string
	Limit  int
	Cursor string
}
type OperationListEntry struct {
	OperationSnapshot
	Title     string
	CreatedAt time.Time
}
type OperationPage struct {
	Items      []OperationListEntry
	NextCursor string
}

var ErrInvalidOperationQuery = errors.New("invalid operation query")

type OperationQuery interface {
	ReadOperation(context.Context, *Actor, uuid.UUID) (*OperationSnapshot, error)
	ListOperations(context.Context, *Actor, OperationListRequest) (*OperationPage, error)
}
