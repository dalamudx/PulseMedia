package personfollow

import (
	"context"
	"errors"
	"math"
	"reflect"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type ConfigService struct{ repo ports.PersonFollowRepository }

func NewConfigService(repo ports.PersonFollowRepository) *ConfigService {
	return &ConfigService{repo: repo}
}

func personActor(a *ports.Actor) (ports.Actor, error) {
	if a == nil {
		return ports.Actor{}, ports.ErrUnauthorized
	}
	current := *a
	id, err := uuid.Parse(current.ID)
	if err != nil || id == uuid.Nil || id.String() != current.ID || current.Type != ports.ActorTypeUser || current.Scopes != nil || (current.Role != ports.RoleAdmin && current.Role != ports.RoleMember) {
		return ports.Actor{}, ports.ErrForbidden
	}
	return current, nil
}
func personKey(key string) bool {
	if len(key) == 0 || len(key) > 160 {
		return false
	}
	for _, b := range []byte(key) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}
func personConfig(c *ports.PersonFollowConfig, owner uuid.UUID) bool {
	return c != nil && c.ID != uuid.Nil && c.OwnerID == owner && c.Version > 0 && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt) && c.Settings.Validate() == nil
}
func personError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, known := range []error{ports.ErrUnavailable, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrPersonFollowConflict} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
func copyPersonConfig(c ports.PersonFollowConfig) ports.PersonFollowConfig {
	c.Settings = c.Settings.Clone()
	return c
}

func (s *ConfigService) Save(ctx context.Context, actor *ports.Actor, id uuid.UUID, input ports.PersonFollowInput, key string) (*ports.PersonFollowReceipt, error) {
	a, err := personActor(actor)
	if err != nil {
		return nil, err
	}
	in := input
	in.Settings = in.Settings.Normalized()
	if id == uuid.Nil || in.Settings.Validate() != nil || in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || !personKey(key) {
		return nil, ports.ErrRejected
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := uuid.MustParse(a.ID)
	readInput := in
	readInput.Settings = in.Settings.Clone()
	result, err := s.repo.SavePersonFollow(ctx, &a, id, readInput, key)
	if err != nil || ctx.Err() != nil {
		return nil, personError(ctx, err)
	}
	if result == nil || result.CommandKey != key || !personConfig(&result.Config, owner) || result.Config.ID != id || result.Config.Version != in.ExpectedVersion+1 {
		return nil, ports.ErrUnavailable
	}
	expected := in.Settings
	if expected.Bindings == nil {
		expected.Bindings = result.Config.Settings.Bindings
	}
	if !reflect.DeepEqual(expected, result.Config.Settings) {
		return nil, ports.ErrUnavailable
	}
	out := *result
	out.Config = copyPersonConfig(result.Config)
	return &out, nil
}

func (s *ConfigService) Read(ctx context.Context, actor *ports.Actor, id uuid.UUID) (*ports.PersonFollowConfig, error) {
	a, err := personActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := uuid.MustParse(a.ID)
	c, err := s.repo.ReadPersonFollow(ctx, &a, id)
	if err != nil || ctx.Err() != nil {
		return nil, personError(ctx, err)
	}
	if !personConfig(c, owner) || c.ID != id {
		return nil, ports.ErrUnavailable
	}
	out := copyPersonConfig(*c)
	return &out, nil
}

func (s *ConfigService) List(ctx context.Context, actor *ports.Actor) ([]ports.PersonFollowConfig, error) {
	a, err := personActor(actor)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := uuid.MustParse(a.ID)
	rows, err := s.repo.ListPersonFollows(ctx, &a)
	if err != nil || ctx.Err() != nil {
		return nil, personError(ctx, err)
	}
	if len(rows) > 100 {
		return nil, ports.ErrUnavailable
	}
	result := make([]ports.PersonFollowConfig, 0, len(rows))
	seen := map[uuid.UUID]bool{}
	for i := range rows {
		if !personConfig(&rows[i], owner) || seen[rows[i].ID] {
			return nil, ports.ErrUnavailable
		}
		seen[rows[i].ID] = true
		result = append(result, copyPersonConfig(rows[i]))
	}
	return result, nil
}

// Receipt never invokes a metadata provider, Save, or subscription creation.
func (s *ConfigService) Receipt(ctx context.Context, actor *ports.Actor, key string) (*ports.PersonFollowReceipt, error) {
	a, err := personActor(actor)
	if err != nil {
		return nil, err
	}
	if !personKey(key) {
		return nil, ports.ErrRejected
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := uuid.MustParse(a.ID)
	r, err := s.repo.ReadPersonFollowReceipt(ctx, &a, key)
	if err != nil || ctx.Err() != nil {
		return nil, personError(ctx, err)
	}
	if r == nil || r.CommandKey != key || !personConfig(&r.Config, owner) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	out.Config = copyPersonConfig(r.Config)
	return &out, nil
}
