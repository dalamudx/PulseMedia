package outbox

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// LoadNotificationDelivery retains LoadOwned's row-lock and DB-clock lease checks
// while keeping database driver types behind the delivery boundary.
func (o *Outbox) LoadNotificationDelivery(ctx context.Context, id uuid.UUID, consumer, owner string, token int64) (*ports.NotificationDelivery, *ports.NotificationEvent, error) {
	delivery, event, err := o.LoadOwned(ctx, id, consumer, owner, token)
	if err != nil {
		return nil, nil, err
	}
	return &ports.NotificationDelivery{EventID: delivery.EventID, AggregateType: delivery.AggregateType, CreatedAt: delivery.CreatedAt.Time},
		&ports.NotificationEvent{ID: event.ID, EventType: event.EventType, AggregateID: event.AggregateID, Payload: append([]byte(nil), event.Payload...)}, nil
}

var _ ports.NotificationDeliveryRepository = (*Outbox)(nil)
var _ ports.DeliveryFailureWriter = (*Outbox)(nil)
