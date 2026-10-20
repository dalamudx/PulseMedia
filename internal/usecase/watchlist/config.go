package watchlist

import (
	"context"
	"errors"
	"math"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type ConfigService struct {
	repo ports.DoubanSyncConfigRepository
}

func NewConfigService(repo ports.DoubanSyncConfigRepository) *ConfigService {
	return &ConfigService{repo: repo}
}

func (s *ConfigService) Read(ctx context.Context, actor *ports.Actor) (*ports.DoubanSyncConfig, error) {
	a, err := syncActor(actor)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	config, err := s.repo.ReadDoubanSyncConfig(ctx, &a)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if config == nil {
		return nil, nil
	}
	if !validConfig(*config, owner) {
		return nil, ports.ErrUnavailable
	}
	out := cloneConfig(*config)
	return &out, nil
}

func (s *ConfigService) Save(ctx context.Context, actor *ports.Actor, input ports.DoubanSyncInput, key string) (*ports.DoubanSyncConfigReceipt, error) {
	a, err := syncActor(actor)
	if err != nil {
		return nil, err
	}
	in := cloneSyncInput(input)
	if !validUID(in.DoubanUID) || in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || !validSyncKey(key) || !validRoots(in.Enabled, in.MovieRootID, in.TVRootID) {
		return nil, ports.ErrRejected
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	// Keep a separate copy for validating the original result, even if a
	// collaborator mutates its input while waiting for database locks.
	readInput := cloneSyncInput(in)
	owner := a.ID
	r, err := s.repo.SaveDoubanSyncConfig(ctx, &a, readInput, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if !validReceipt(r, key, owner) || r.Config.Version != in.ExpectedVersion+1 || r.Config.DoubanUID != in.DoubanUID || r.Config.Enabled != in.Enabled || !equalRoot(r.Config.MovieRootID, in.MovieRootID) || !equalRoot(r.Config.TVRootID, in.TVRootID) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	out.Config = cloneConfig(r.Config)
	return &out, nil
}

// Receipt is read-only; it never calls Save or requests an upstream list.
func (s *ConfigService) Receipt(ctx context.Context, actor *ports.Actor, key string) (*ports.DoubanSyncConfigReceipt, error) {
	a, err := syncActor(actor)
	if err != nil {
		return nil, err
	}
	if !validSyncKey(key) {
		return nil, ports.ErrRejected
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	r, err := s.repo.ReadDoubanSyncConfigReceipt(ctx, &a, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, configError(err)
	}
	if !validReceipt(r, key, owner) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	out.Config = cloneConfig(r.Config)
	return &out, nil
}

func syncActor(actor *ports.Actor) (ports.Actor, error) {
	if actor == nil {
		return ports.Actor{}, ports.ErrUnauthorized
	}
	a := *actor
	id, err := uuid.Parse(a.ID)
	if err != nil || id == uuid.Nil || id.String() != a.ID || a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) {
		return ports.Actor{}, ports.ErrForbidden
	}
	return a, nil
}

func validUID(uid string) bool {
	if len(uid) == 0 || len(uid) > 64 {
		return false
	}
	for i, c := range []byte(uid) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '_' && c != '-' && c != '.') {
			return false
		}
	}
	return true
}

func validSyncKey(key string) bool {
	if len(key) == 0 || len(key) > 160 {
		return false
	}
	for _, c := range []byte(key) {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '-' || c == '.'
		if !allowed {
			return false
		}
	}
	return true
}

func validRoots(enabled bool, movie, tv *uuid.UUID) bool {
	return (movie == nil || *movie != uuid.Nil) && (tv == nil || *tv != uuid.Nil) && (!enabled || movie != nil || tv != nil) && (movie == nil || tv == nil || *movie != *tv)
}

func validConfig(c ports.DoubanSyncConfig, owner string) bool {
	return c.ID != uuid.Nil && c.OwnerID.String() == owner && validUID(c.DoubanUID) && c.Version > 0 && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt) && validRoots(c.Enabled, c.MovieRootID, c.TVRootID)
}

func validReceipt(r *ports.DoubanSyncConfigReceipt, key, owner string) bool {
	return r != nil && r.CommandKey == key && validConfig(r.Config, owner)
}
func equalRoot(a, b *uuid.UUID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func cloneRoot(id *uuid.UUID) *uuid.UUID {
	if id == nil {
		return nil
	}
	out := *id
	return &out
}
func cloneConfig(c ports.DoubanSyncConfig) ports.DoubanSyncConfig {
	c.MovieRootID, c.TVRootID = cloneRoot(c.MovieRootID), cloneRoot(c.TVRootID)
	return c
}
func cloneSyncInput(in ports.DoubanSyncInput) ports.DoubanSyncInput {
	in.MovieRootID, in.TVRootID = cloneRoot(in.MovieRootID), cloneRoot(in.TVRootID)
	return in
}
func configError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrRejected, ports.ErrNotFound, ports.ErrSubscriptionBindingsConflict, ports.ErrUnavailable} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	// Database and provider details are never part of a configuration response.
	return ports.ErrUnavailable
}
