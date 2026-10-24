// Package seeding configures downloader-scoped eligibility without removal authority.
package seeding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"

	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type ConfigService struct{ repo ports.SeedingConfigRepository }

func NewConfigService(repo ports.SeedingConfigRepository) *ConfigService {
	return &ConfigService{repo: repo}
}
func configActor(ctx context.Context, a *ports.Actor) (ports.Actor, error) {
	if err := ctx.Err(); err != nil {
		return ports.Actor{}, err
	}
	if a == nil {
		return ports.Actor{}, ports.ErrUnauthorized
	}
	id, err := uuid.Parse(a.ID)
	if err != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.Actor{}, ports.ErrForbidden
	}
	return *a, nil
}
func commandKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == ':', r == '.':
		default:
			return false
		}
	}
	return true
}
func configError(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrSeedingConfigConflict} {
		if errors.Is(err, e) {
			return e
		}
	}
	return ports.ErrUnavailable
}
func cloneRule(in rule.Rule) rule.Rule {
	out := in
	if in.MinSeedingSeconds != nil {
		n := *in.MinSeedingSeconds
		out.MinSeedingSeconds = &n
	}
	if in.MinShareRatio != nil {
		n := *in.MinShareRatio
		out.MinShareRatio = &n
	}
	if in.LowFreeBytes != nil {
		n := *in.LowFreeBytes
		out.LowFreeBytes = &n
	}
	return out
}
func validView(v *ports.SeedingConfigView, client uuid.UUID) bool {
	return v != nil && v.ClientID == client && v.OwnerID != uuid.Nil && v.Version > 0 && !v.ClientUpdatedAt.IsZero() && v.IntervalSeconds >= 60 && v.IntervalSeconds <= 86400 && !v.UpdatedAt.IsZero() && v.Rule.Validate() == nil
}
func cloneView(v ports.SeedingConfigView) ports.SeedingConfigView {
	v.Rule = cloneRule(v.Rule)
	v.UpdatedAt = v.UpdatedAt.UTC()
	v.ClientUpdatedAt = v.ClientUpdatedAt.UTC()
	return v
}
func validReceipt(v *ports.SeedingConfigReceipt, a ports.Actor, client uuid.UUID, key string) bool {
	return v != nil && v.CommandKey == key && validView(&v.Config, client) && v.Config.OwnerID.String() == a.ID
}
func cloneReceipt(v ports.SeedingConfigReceipt) *ports.SeedingConfigReceipt {
	v.Config = cloneView(v.Config)
	return &v
}
func (s *ConfigService) Read(ctx context.Context, actor *ports.Actor, client uuid.UUID) (*ports.SeedingConfigView, error) {
	a, err := configActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if client == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	v, err := s.repo.ReadSeedingConfig(ctx, &a, client)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if v == nil {
		return nil, nil
	}
	if !validView(v, client) {
		return nil, ports.ErrUnavailable
	}
	out := cloneView(*v)
	return &out, nil
}
func (s *ConfigService) Receipt(ctx context.Context, actor *ports.Actor, client uuid.UUID, key string) (*ports.SeedingConfigReceipt, error) {
	a, err := configActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if client == uuid.Nil || !commandKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	v, err := s.repo.ReadSeedingConfigReceipt(ctx, &a, client, key, nil)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if v == nil {
		return nil, ports.ErrNotFound
	}
	if !validReceipt(v, a, client, key) {
		return nil, ports.ErrUnavailable
	}
	return cloneReceipt(*v), nil
}
func (s *ConfigService) Save(ctx context.Context, actor *ports.Actor, client uuid.UUID, in ports.SeedingConfigInput, key string) (*ports.SeedingConfigReceipt, error) {
	a, err := configActor(ctx, actor)
	if err != nil {
		return nil, err
	}
	if client == uuid.Nil || !commandKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	in.Rule = cloneRule(in.Rule)
	in.ExpectedClientUpdatedAt = in.ExpectedClientUpdatedAt.UTC()
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, ports.ErrRejected
	}
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	original, err := s.repo.ReadSeedingConfigReceipt(ctx, &a, client, key, &fingerprint)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if original != nil {
		if !validReceipt(original, a, client, key) {
			return nil, ports.ErrUnavailable
		}
		return cloneReceipt(*original), nil
	}
	if in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || in.ExpectedClientUpdatedAt.IsZero() || in.IntervalSeconds < 60 || in.IntervalSeconds > 86400 || in.Rule.Validate() != nil {
		return nil, ports.ErrRejected
	}
	result, err := s.repo.SaveSeedingConfig(ctx, &a, client, in, key, fingerprint)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if !validReceipt(result, a, client, key) {
		return nil, ports.ErrUnavailable
	}
	return cloneReceipt(*result), nil
}
