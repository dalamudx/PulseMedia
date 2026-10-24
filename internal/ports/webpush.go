package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/webpush"
	"github.com/google/uuid"
	"time"
)

var ErrWebPushConflict = errors.New("web push subscription conflict")

type WebPushSubscription = webpush.Subscription

type WebPushPublicConfig struct {
	Enabled   bool   `json:"enabled"`
	Version   string `json:"version"`
	PublicKey string `json:"public_key"`
}

type WebPushDevice struct {
	ID        uuid.UUID `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WebPushRegistration struct {
	ID             uuid.UUID
	VAPIDVersion   string
	EndpointDigest string
	Ciphertext     []byte
}

// Preparing credentials happens only for new commands, after receipt recovery.
type WebPushRegistrationPrepare func() (WebPushRegistration, error)

type WebPushRepository interface {
	RegisterWebPush(context.Context, *Actor, uuid.UUID, string, string, WebPushRegistrationPrepare) (*WebPushDevice, error)
	RevokeWebPush(context.Context, *Actor, uuid.UUID, time.Time, string, string) (*WebPushDevice, error)
	ListWebPush(context.Context, *Actor) ([]WebPushDevice, error)
	GetWebPushReceipt(context.Context, *Actor, string) (*WebPushDevice, error)
}
