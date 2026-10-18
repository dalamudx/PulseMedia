package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"regexp"
	"strings"
)

var ErrPolicyNotFound = ports.ErrNotFound
var ErrRevisionNotFound = ports.ErrNotFound

type Service struct{ repo ports.PolicyRepository }

func NewService(repo ports.PolicyRepository) *Service { return &Service{repo} }
func (s *Service) List(ctx context.Context) ([]ports.PolicyDocument, error) {
	return s.repo.ListPolicies(ctx)
}
func (s *Service) Read(ctx context.Context, id string, rev int64) (*ports.PolicyDocument, error) {
	if rev < 0 || !validID(id) {
		return nil, ports.ErrInvalidPolicy
	}
	return s.repo.ReadPolicy(ctx, id, rev)
}
func (s *Service) GetRevision(ctx context.Context, id string, rev int64) (*quality.PolicyRules, error) {
	p, e := s.Read(ctx, id, rev)
	if e != nil {
		return nil, e
	}
	return &p.Rules, nil
}
func validID(id string) bool { return regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(id) }
func (s *Service) Command(ctx context.Context, a *ports.Actor, c ports.PolicyCommand, key string) (*ports.PolicyReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if !validID(c.ID) || key == "" || len(key) > 160 || c.ExpectedRevision < 0 {
		return nil, ports.ErrInvalidPolicy
	}
	switch c.Kind {
	case "create":
		if c.ExpectedRevision != 0 || strings.TrimSpace(c.Name) == "" || len(c.Name) > 128 || c.Rules == nil || c.RestoreRevision != nil {
			return nil, ports.ErrInvalidPolicy
		}
	case "publish":
		if c.ExpectedRevision < 1 || c.Rules == nil || c.RestoreRevision != nil || c.Name != "" {
			return nil, ports.ErrInvalidPolicy
		}
	case "restore":
		if c.ExpectedRevision < 1 || c.RestoreRevision == nil || *c.RestoreRevision < 1 || c.Rules != nil || c.Name != "" {
			return nil, ports.ErrInvalidPolicy
		}
	default:
		return nil, ports.ErrInvalidPolicy
	}
	if c.Rules != nil && quality.ValidateRules(*c.Rules) != nil {
		return nil, ports.ErrInvalidPolicy
	}
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return s.repo.WritePolicy(ctx, a, c, key, hex.EncodeToString(sum[:]))
}

// Legacy internal administrative helpers have no HTTP registration. Commands
// exposed to clients require an explicit expected revision and idempotency key.
func (s *Service) PublishNewRevision(ctx context.Context, a *ports.Actor, id string, r quality.PolicyRules) (int64, error) {
	if a == nil {
		return 0, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return 0, ports.ErrForbidden
	}
	if !a.IsAdmin() {
		return 0, ports.ErrForbidden
	}
	if quality.ValidateRules(r) != nil {
		return 0, ports.ErrInvalidPolicy
	}
	p, e := s.repo.WritePolicy(ctx, a, ports.PolicyCommand{Kind: "publish", ID: id, ExpectedRevision: -1, Rules: &r}, "", "")
	if e != nil {
		return 0, e
	}
	return p.Revision, nil
}
func (s *Service) RollbackToRevision(ctx context.Context, a *ports.Actor, id string, rev int64) (int64, error) {
	if a == nil {
		return 0, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return 0, ports.ErrForbidden
	}
	if !a.IsAdmin() {
		return 0, ports.ErrForbidden
	}
	p, e := s.repo.WritePolicy(ctx, a, ports.PolicyCommand{Kind: "restore", ID: id, ExpectedRevision: -1, RestoreRevision: &rev}, "", "")
	if e != nil {
		return 0, e
	}
	return p.Revision, nil
}
func (s *Service) Preview(r quality.PolicyRules, title string, p quality.Profile) quality.EvaluationPreview {
	return quality.EvaluateCandidate(r, title, p)
}
