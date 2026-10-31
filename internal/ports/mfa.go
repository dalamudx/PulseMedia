package ports

import (
	"context"
	"github.com/google/uuid"
	"time"
)

type MFAProof struct {
	OTP          string `json:"otp,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}
type MFAReauthentication struct {
	CurrentPassword string `json:"current_password"`
	MFAProof
}
type MFAState struct {
	Enabled     bool
	Secret      []byte
	LastCounter int64
	ChallengeID uuid.UUID
	ExpiresAt   time.Time
}
type MFAStatus struct {
	Enabled                bool `json:"enabled"`
	RecoveryCodesRemaining int  `json:"recovery_codes_remaining"`
}
type MFASetupResult struct {
	ChallengeID uuid.UUID `json:"challenge_id"`
	Secret      string    `json:"secret,omitempty"`
	OTPAuthURL  string    `json:"otpauth_url,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	Replayed    bool      `json:"replayed"`
}
type MFAConfirmResult struct {
	Enabled       bool     `json:"enabled"`
	RecoveryCodes []string `json:"recovery_codes,omitempty"`
	Replayed      bool     `json:"replayed"`
}

// MFATransaction uses the same locked user and transaction as session issuance.
// A failed outer operation must roll back counter or recovery-code consumption.
type MFATransaction interface {
	LoadMFA(context.Context) (MFAState, error)
	StoreMFA(context.Context, MFAState) error
	ConsumeRecoveryCode(context.Context, []byte) (bool, error)
	ReplaceRecoveryCodes(context.Context, [][]byte) error
	MFAStatus(context.Context) (MFAStatus, error)
	DeleteMFA(context.Context) error
}
