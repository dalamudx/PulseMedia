package ports

import (
	"context"
	"github.com/google/uuid"
)

type WebPushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Icon  string `json:"icon,omitempty"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
}
type WebPushSender interface {
	SendNotification(context.Context, WebPushSubscription, WebPushPayload) error
}
type WebPushOwnedDelivery struct {
	EventID    uuid.UUID
	Ciphertext []byte
}

// Execution retains physical exclusivity until SendNotification has returned.
type WebPushDeliveryExecution interface {
	Load(context.Context, uuid.UUID, string, int64, string) (*WebPushOwnedDelivery, error)
	Complete(context.Context, uuid.UUID, string, int64, string) error
	Close() error
}
type WebPushDeliveryRepository interface {
	OpenWebPushExecution(context.Context, uuid.UUID) (WebPushDeliveryExecution, error)
}
