package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrInvalidChannelState = errors.New("invalid notification channel state command")
var ErrChannelStateConflict = errors.New("notification channel state conflict")

type ChannelStateCommand struct {
	ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	IsEnabled         bool      `json:"is_enabled"`
}
type ChannelStateReceipt struct {
	ID        uuid.UUID `json:"id"`
	IsEnabled bool      `json:"is_enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}
type ChannelStateRepository interface {
	SetChannelState(context.Context, *Actor, uuid.UUID, ChannelStateCommand, string, string) (*ChannelStateReceipt, error)
}
