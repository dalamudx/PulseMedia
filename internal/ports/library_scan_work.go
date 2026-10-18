package ports

import (
	"context"
	"github.com/google/uuid"
)

// LibraryScanWork binds a claimed task to its persisted root and phase.
type LibraryScanWork struct {
	Lease  JobLease
	RootID uuid.UUID
	Phase  string
	RunID  uuid.UUID
}

type LibraryScanSchedulingRepository interface {
	QueueDue(context.Context, int32) (int, error)
}
