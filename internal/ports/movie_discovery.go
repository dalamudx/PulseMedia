package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type MovieDiscoveryStatus struct {
	SubscriptionID uuid.UUID  `json:"subscription_id"`
	SearchID       *uuid.UUID `json:"search_id,omitempty"`
	CurrentFileID  *uuid.UUID `json:"current_file_id,omitempty"`

	NextDiscoveryAt  *time.Time `json:"next_discovery_at,omitempty"`
	State            string     `json:"state"`
	ExecutionAllowed bool       `json:"execution_allowed"`
}

// Scheduling creates observation jobs only. The store rechecks current account,
// policy, intent, root and imported-file evidence inside the scheduling transaction.
type MovieDiscoveryRepository interface {
	ListDueMovieDiscoveries(context.Context, int32) ([]uuid.UUID, error)
	ScheduleMovieDiscovery(context.Context, uuid.UUID) (bool, error)
	ReadMovieDiscovery(context.Context, *Actor, uuid.UUID) (*MovieDiscoveryStatus, error)
}
