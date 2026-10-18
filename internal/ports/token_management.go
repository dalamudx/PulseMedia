package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrInvalidTokenCommand = errors.New("invalid token command")
var ErrTokenCommandConflict = errors.New("token command conflict")

type ManagedToken struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}
type TokenCommand struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
type TokenCommandResult struct {
	Item     ManagedToken `json:"item"`
	Replayed bool         `json:"replayed"`
}
type TokenManagementRepository interface {
	ListOwnTokens(context.Context, *Actor) ([]ManagedToken, error)
	CreateOwnToken(context.Context, *Actor, TokenCommand, []byte, time.Time, string, string) (*TokenCommandResult, error)
	RevokeOwnToken(context.Context, *Actor, uuid.UUID, string, string) (*TokenCommandResult, error)
}
type ReadTokenIssuer interface {
	IssueReadToken() (plain string, hash []byte, err error)
}
