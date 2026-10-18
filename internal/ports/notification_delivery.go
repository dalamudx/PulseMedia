package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

// CredentialDecoder opens credentials sealed with the system master key.
type CredentialDecoder interface{ DecryptSecret([]byte) ([]byte, error) }

// NotificationCredentialCodec preserves the existing ciphertext and keyed intent digest formats.
type NotificationCredentialCodec interface {
	SecretProtector
	CredentialDecoder
}

type NotificationChannelConnection struct {
	ID              uuid.UUID
	ChannelType     string
	IsEnabled       bool
	ConfigEncrypted []byte
}

type NotificationChannelReader interface {
	LoadNotificationChannel(context.Context, uuid.UUID) (*NotificationChannelConnection, error)
}

type NotificationDelivery struct {
	EventID       uuid.UUID
	AggregateType string
	CreatedAt     time.Time
}

type NotificationEvent struct {
	ID          uuid.UUID
	EventType   string
	AggregateID string
	Payload     []byte
}

// DeliveryFailureWriter records a fenced failure after external I/O has returned.
type DeliveryFailureWriter interface {
	Fail(context.Context, uuid.UUID, string, int64, string, time.Duration) error
}

// NotificationDeliveryRepository validates the current lease before each send.
type NotificationDeliveryRepository interface {
	DeliveryFailureWriter
	LoadNotificationDelivery(context.Context, uuid.UUID, string, string, int64) (*NotificationDelivery, *NotificationEvent, error)
	ReturnDisabledNotification(context.Context, uuid.UUID, string, string, int64) error
	Finish(context.Context, uuid.UUID, string, int64) error
}
