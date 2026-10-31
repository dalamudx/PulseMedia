package ports

import (
	"context"
	"github.com/google/uuid"
)

// SubscriptionDetailsReader returns one current-account, read-only snapshot.
type SubscriptionDetailsReader interface {
	ReadSubscriptionDetails(context.Context, *Actor, uuid.UUID) (*SubscriptionDetails, error)
}

type SubscriptionDetails struct {
	SubscriptionListEntry
	SelectedUnitIDs []uuid.UUID
	Seasons         []int
	IncludeSpecials bool
}
