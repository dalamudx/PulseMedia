package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/google/uuid"
	"time"
)

type SeerrConfiguration struct {
	Enabled           bool       `json:"enabled"`
	Version           int64      `json:"version"`
	OwnerID           *uuid.UUID `json:"owner_id,omitempty"`
	CredentialPresent bool       `json:"credential_present"`
}
type SeerrConfigurationInput struct {
	Enabled         bool      `json:"enabled"`
	ExpectedVersion int64     `json:"expected_version"`
	OwnerID         uuid.UUID `json:"owner_id"`
	Credential      string    `json:"credential,omitempty"`
}
type SeerrEvent struct {
	ExternalRequestID string
	PayloadHash       string
	Intent            mediarequest.Intent
}
type SeerrReceipt struct {
	ExternalRequestID string    `json:"external_request_id"`
	RequestID         uuid.UUID `json:"request_id"`
	ConfigVersion     int64     `json:"config_version"`
	State             string    `json:"state"`
	CreatedAt         time.Time `json:"created_at"`
}
type SeerrRepository interface {
	ReadSeerrConfiguration(context.Context, *Actor) (SeerrConfiguration, error)
	SaveSeerrConfiguration(context.Context, *Actor, SeerrConfigurationInput) (SeerrConfiguration, error)
	ReceiveSeerr(context.Context, string, SeerrEvent) (SeerrReceipt, error)
	ListSeerrReceipts(context.Context, *Actor) ([]SeerrReceipt, error)
}

// SecretCipher extends the existing deployment protector; secrets never enter receipts.
type SecretCipher interface {
	SecretProtector
	DecryptSecret([]byte) ([]byte, error)
}
