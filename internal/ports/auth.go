package ports

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

var (
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrAlreadyInitialized  = errors.New("system administrator has already been initialized")
	ErrUserDisabled        = errors.New("user account is disabled")
	ErrInvalidCredentials  = errors.New("invalid username or password")
	ErrTokenExpired        = errors.New("authentication token has expired")
	ErrTokenRevoked        = errors.New("authentication token has been revoked")
	ErrSystemImpersonation = errors.New("external tokens cannot impersonate system actor")
)

// User represents an authenticated identity.
type User struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

// Session represents an active authenticated web session.
type Session struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"user_id"`
	TokenHash     []byte    `json:"-"`
	CsrfTokenHash []byte    `json:"-"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

// APIToken represents a programmatic scoped token.
type APIToken struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"user_id"`
	Name      string     `json:"name"`
	TokenHash []byte     `json:"-"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Actor permission checks
func (a *Actor) IsAdmin() bool {
	return a != nil && a.Role == RoleAdmin
}

func (a *Actor) IsSystem() bool {
	return a != nil && a.Type == ActorTypeSystem
}

func (a *Actor) CanAccessUser(targetUserID string) bool {
	if a == nil {
		return false
	}
	if a.IsAdmin() || a.IsSystem() {
		return true
	}
	return a.ID == targetUserID
}
