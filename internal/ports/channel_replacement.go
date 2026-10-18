package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrInvalidChannelReplacement = errors.New("invalid notification channel replacement")
var ErrChannelReplacementConflict = errors.New("notification channel replacement conflict")

type NotificationChannelConfig struct {
	URL      string `json:"url,omitempty"`
	Secret   string `json:"secret,omitempty"`
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
}
type ChannelReplacementCommand struct {
	ExpectedUpdatedAt time.Time                 `json:"expected_updated_at"`
	Name              string                    `json:"name"`
	ChannelType       string                    `json:"channel_type"`
	Config            NotificationChannelConfig `json:"config"`
	AcceptOldPending  bool                      `json:"accept_old_pending"`
}

// Store receives ciphertext and keyed digest, never the secret-bearing HTTP command.
type ChannelReplacementWrite struct {
	ExpectedUpdatedAt time.Time
	Name, ChannelType string
	Ciphertext        []byte
}
type ChannelReplacementReceipt struct {
	OldChannelID uuid.UUID `json:"old_channel_id"`
	NewChannelID uuid.UUID `json:"new_channel_id"`
	OldEnabled   bool      `json:"old_enabled"`
	NewEnabled   bool      `json:"new_enabled"`
	OldUpdatedAt time.Time `json:"old_updated_at"`
	NewUpdatedAt time.Time `json:"new_updated_at"`
}
type ChannelReplacementRepository interface {
	ReplaceNotificationChannel(context.Context, *Actor, uuid.UUID, ChannelReplacementWrite, string, string) (*ChannelReplacementReceipt, error)
}
