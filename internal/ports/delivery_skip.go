package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type DeliverySkipCommand struct {
	ExpectedToken      int64     `json:"expected_token"`
	ExpectedAttempts   int       `json:"expected_attempts"`
	ExpectedUpdatedAt  time.Time `json:"expected_updated_at"`
	AcceptDeliveryLoss bool      `json:"accept_delivery_loss"`
	Reason             string    `json:"reason"`
}
type DeliverySkipReceipt struct {
	ID         uuid.UUID `json:"id"`
	EventID    uuid.UUID `json:"event_id"`
	State      string    `json:"state"`
	LeaseToken int64     `json:"lease_token"`
	Reason     string    `json:"reason"`
	SkippedAt  time.Time `json:"skipped_at"`
}
type DeliverySkipRepository interface {
	SkipIntegrationDelivery(context.Context, *Actor, uuid.UUID, DeliverySkipCommand, string, string) (*DeliverySkipReceipt, error)
}
