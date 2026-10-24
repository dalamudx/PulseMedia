package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type NotificationTestReceipt struct {
	ChannelID uuid.UUID `json:"channel_id"`
	EventID   uuid.UUID `json:"event_id"`
	Status    string    `json:"status"`
}
type NotificationTestRepository interface {
	QueueNotificationTest(context.Context, *Actor, uuid.UUID, time.Time, string, string) (*NotificationTestReceipt, error)
}
