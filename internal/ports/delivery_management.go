package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrInvalidDeliveryCommand = errors.New("invalid integration delivery command")
var ErrDeliveryCommandConflict = errors.New("integration delivery command conflict")

type DeliverySummary struct {
	ID            uuid.UUID `json:"id"`
	EventID       uuid.UUID `json:"event_id"`
	Consumer      string    `json:"consumer"`
	AggregateType string    `json:"aggregate_type"`
	AggregateID   string    `json:"aggregate_id"`
	Sequence      int64     `json:"sequence"`
	State         string    `json:"state"`
	Attempts      int       `json:"attempts"`
	LeaseToken    int64     `json:"lease_token"`
	UpdatedAt     time.Time `json:"updated_at"`
	NotBefore     time.Time `json:"not_before"`
	RecipientName string    `json:"recipient_name"`
	DuplicateRisk string    `json:"duplicate_risk"`
}
type DeliveryReplayCommand struct {
	ExpectedToken       int64     `json:"expected_token"`
	ExpectedAttempts    int       `json:"expected_attempts"`
	ExpectedUpdatedAt   time.Time `json:"expected_updated_at"`
	AcceptDuplicateRisk bool      `json:"accept_duplicate_risk"`
}
type DeliveryReplayReceipt struct {
	ID         uuid.UUID `json:"id"`
	EventID    uuid.UUID `json:"event_id"`
	State      string    `json:"state"`
	LeaseToken int64     `json:"lease_token"`
}
type DeliveryListQuery struct {
	Cursor  string
	State   string
	EventID uuid.UUID
	Limit   int
}
type DeliveryListPage struct {
	Items      []DeliverySummary
	Limit      int
	NextCursor string
}
type DeliveryManagementRepository interface {
	ListIntegrationDeliveries(context.Context, *Actor, DeliveryListQuery) (*DeliveryListPage, error)
	ReplayIntegrationDelivery(context.Context, *Actor, uuid.UUID, DeliveryReplayCommand, string, string) (*DeliveryReplayReceipt, error)
}
