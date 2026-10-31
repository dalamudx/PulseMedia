package ports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"time"
)

var (
	ErrInvalidAccountCommand  = errors.New("invalid account command")
	ErrAccountCommandConflict = errors.New("account command conflict")
	ErrLastActiveAdmin        = errors.New("last active administrator must remain active")
)

type SecurityIdentity struct {
	Actor       *Actor
	SessionHash []byte
}
type AccountCreate struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}
type AccountUpdate struct {
	Role     string `json:"role"`
	IsActive bool   `json:"is_active"`
}
type PasswordChange struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	MFAProof
}
type SecurityCommandReceipt struct {
	Action string          `json:"action"`
	Result json.RawMessage `json:"result"`
}

type AccountCommandResult struct {
	User      *User      `json:"user,omitempty"`
	SessionID *uuid.UUID `json:"session_id,omitempty"`
	Replayed  bool       `json:"replayed"`
}
type ManagedSession struct {
	ID         uuid.UUID `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

// SecurityCipher computes keyed command digests so password guesses cannot be checked
// against command receipts without the application master key.
type SecurityCipher interface {
	SettingsCipher
	CommandDigest([]byte) string
}

// AccountManagementTx is valid only within its callback. The store locks and
// revalidates the current account AND session before exposing any receipt or data.
// Administrative boundaries serialize account changes and lock users in UUID order.
type AccountManagementTx interface {
	MFATransaction
	Account() AuthenticationAccount
	SessionID() uuid.UUID
	ListAccounts(context.Context) ([]User, error)
	CreateAccount(context.Context, uuid.UUID, string, string, string) (User, error)
	UpdateAccount(context.Context, uuid.UUID, AccountUpdate) (User, error)
	ListSessions(context.Context) ([]ManagedSession, error)
	RevokeSession(context.Context, uuid.UUID) error
	ChangePassword(context.Context, string) error
	ResetMemberPassword(context.Context, uuid.UUID, string) (User, error)
	RevokeOtherSessions(context.Context) error
	RevokeOtherCredentials(context.Context) error
	ReadCommand(context.Context, string) (*SecurityCommandReceipt, error)
	Receipt(context.Context, string, string, any) (bool, error)
	Finish(context.Context, string, string, string, uuid.UUID, json.RawMessage) error
}
type AccountManagementRepository interface {
	WithAccountManagement(context.Context, SecurityIdentity, bool, func(AccountManagementTx) error) error
}
