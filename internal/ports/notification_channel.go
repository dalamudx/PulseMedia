package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrChannelCreateConflict = errors.New("notification channel command idempotency conflict")

type NotificationChannelObservation struct {
	ID                uuid.UUID
	Name, ChannelType string
	IsEnabled         bool
	UpdatedAt         time.Time
	ReplacedBy        *uuid.UUID
}
type NotificationChannelWrite struct {
	Name, ChannelType string
	Ciphertext        []byte
}
type NotificationChannelRepository interface {
	CreateNotificationChannel(context.Context, *Actor, NotificationChannelWrite, string, string) (*NotificationChannelObservation, error)
	ListNotificationChannels(context.Context, *Actor) ([]NotificationChannelObservation, error)
}
