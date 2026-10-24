package subscription

import (
	"context"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type PersonSubscription struct {
	ID         uuid.UUID `json:"id"`
	PersonID   int64     `json:"person_id"`
	PersonName string    `json:"person_name"`
	Role       string    `json:"role"`
}

// PersonSubscriptionManager preserves the legacy entry until persistent approval exists.
type PersonSubscriptionManager struct{}

func NewPersonSubscriptionManager(_ *Service, _ ports.MetadataProvider) *PersonSubscriptionManager {
	return &PersonSubscriptionManager{}
}

// AutoSubscribeNewRelease cannot claim success without a committed subscription.
func (*PersonSubscriptionManager) AutoSubscribeNewRelease(ctx context.Context, _ *ports.Actor, _ PersonSubscription, _ *ports.MovieMetadata, _ uuid.UUID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, ports.ErrUnavailable
}
