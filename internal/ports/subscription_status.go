package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/google/uuid"
)

// ReadSubscriptionStatus observes one authorized, read-only database snapshot.
// It exposes no transaction, network clients, paths, credentials or operations to the caller.
type SubscriptionStatusRepository interface {
	ReadSubscriptionStatus(context.Context, *Actor, uuid.UUID) (subscription.Snapshot, error)
}
