package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrAuthenticationNotFound = errors.New("authentication record not found")

// AuthenticationSecrets uses the system's canonical password profile and token hashes.
// VerifyPassword must reject malformed or unsupported stored hashes before deriving keys.
type AuthenticationSecrets interface {
	HashPassword(string) (string, error)
	VerifyPassword(string, string) bool
	GenerateToken(string) (string, []byte, error)
	HashToken(string) []byte
}

type AuthenticationAccount struct {
	User         User
	PasswordHash string
}

type AuthenticationSession struct {
	ID        uuid.UUID
	User      User
	ExpiresAt time.Time
}

type AuthenticationToken struct {
	User       User
	ScopesJSON []byte
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// AuthenticationSessionWriter saves only hashes and participates in its enclosing
// setup/login transaction. Implementations must never retain the callback handle.
type AuthenticationSessionWriter interface {
	CreateSession(context.Context, Session) error
}

type AuthenticationSetup interface {
	AuthenticationSessionWriter
	IsInitialized(context.Context) (bool, error)
	CreateAdmin(context.Context, uuid.UUID, string, string) (User, error)
}

// AuthenticationRepository exposes two narrow atomic issuance boundaries. Setup
// holds the first-admin advisory lock before invoking fn. Login locks and re-reads
// the current account before invoking fn; callback failure rolls back all writes.
// Lookups return ErrAuthenticationNotFound for absent/DB-expired credentials and
// ErrUnavailable for storage failures. Credential lookups must filter current
// active users and require expires_at > database time (NULL API expiry is valid).
type AuthenticationRepository interface {
	IsInitialized(context.Context) (bool, error)
	WithAdminSetup(context.Context, func(AuthenticationSetup) error) error
	FindAccount(context.Context, string) (AuthenticationAccount, error)
	WithLoginAccount(context.Context, uuid.UUID, func(AuthenticationAccount, AuthenticationSessionWriter) error) error
	FindSession(context.Context, []byte) (AuthenticationSession, error)
	FindAPIToken(context.Context, []byte) (AuthenticationToken, error)
	TouchSession(context.Context, uuid.UUID) error
	DeleteSession(context.Context, uuid.UUID) error
}
