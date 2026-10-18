package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"unicode"
	"unicode/utf8"
)

type TokenManagementService struct {
	repo   ports.TokenManagementRepository
	issuer ports.ReadTokenIssuer
	clock  ports.Clock
}

func NewTokenManagementService(repo ports.TokenManagementRepository, issuer ports.ReadTokenIssuer, clock ports.Clock) *TokenManagementService {
	return &TokenManagementService{repo, issuer, clock}
}
func tokenSession(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) {
		return ports.ErrForbidden
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil {
		return ports.ErrForbidden
	}
	return nil
}
func tokenFingerprint(action string, v any) string {
	b, _ := json.Marshal([]any{action, v})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *TokenManagementService) List(ctx context.Context, a *ports.Actor) ([]ports.ManagedToken, error) {
	if e := tokenSession(a); e != nil {
		return nil, e
	}
	return s.repo.ListOwnTokens(ctx, a)
}
func (s *TokenManagementService) Create(ctx context.Context, a *ports.Actor, c ports.TokenCommand, key string) (*ports.TokenCommandResult, string, error) {
	if e := tokenSession(a); e != nil {
		return nil, "", e
	}
	if key == "" || len(key) > 160 || !utf8.ValidString(c.Name) || strings.TrimSpace(c.Name) != c.Name || c.Name == "" || len(c.Name) > 120 || strings.IndexFunc(c.Name, unicode.IsControl) >= 0 {
		return nil, "", ports.ErrInvalidTokenCommand
	}
	if c.ExpiresAt != nil {
		v := c.ExpiresAt.UTC()
		c.ExpiresAt = &v
	}
	plain, hash, e := s.issuer.IssueReadToken()
	if e != nil {
		return nil, "", ports.ErrUnavailable
	}
	out, e := s.repo.CreateOwnToken(ctx, a, c, hash, s.clock.Now(), key, tokenFingerprint("auth.token.create.v1", c))
	if e != nil {
		return nil, "", e
	}
	if out.Replayed {
		plain = ""
	}
	return out, plain, nil
}
func (s *TokenManagementService) Revoke(ctx context.Context, a *ports.Actor, id uuid.UUID, key string) (*ports.TokenCommandResult, error) {
	if e := tokenSession(a); e != nil {
		return nil, e
	}
	if id == uuid.Nil || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidTokenCommand
	}
	return s.repo.RevokeOwnToken(ctx, a, id, key, tokenFingerprint("auth.token.revoke.v1", id))
}
