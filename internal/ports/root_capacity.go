package ports

import (
	"context"
	"errors"
	"time"
)

var ErrCapacityUnavailable = errors.New("filesystem capacity unavailable")

// RootCapacity is an observation of a pinned directory's filesystem. It grants
// neither root health nor ownership. AvailableBytes uses unprivileged Bavail.
type RootCapacity struct {
	TotalBytes     int64
	AvailableBytes int64
	SampledAt      time.Time
}

// RootCapacitySampler performs physical I/O under the caller's existing root
// slot. Cancellation never permits returning before the physical call ends.
type RootCapacitySampler interface {
	SampleCapacity(context.Context, TransferRoot) (RootCapacity, error)
}
