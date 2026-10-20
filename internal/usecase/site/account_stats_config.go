package site

import (
	"context"
	"errors"
	"math"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type AccountStatsConfigService struct {
	repo ports.SiteAccountStatsConfigRepository
}

func NewAccountStatsConfigService(repo ports.SiteAccountStatsConfigRepository) *AccountStatsConfigService {
	return &AccountStatsConfigService{repo: repo}
}

func accountStatsConfigActor(ctx context.Context, actor *ports.Actor, source uuid.UUID) (ports.Actor, error) {
	if actor == nil {
		return ports.Actor{}, ports.ErrUnauthorized
	}
	a := *actor
	id, err := uuid.Parse(a.ID)
	if err != nil || id == uuid.Nil || id.String() != a.ID || a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.Actor{}, ports.ErrForbidden
	}
	if source == uuid.Nil {
		return ports.Actor{}, ports.ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return ports.Actor{}, err
	}
	return a, nil
}

func accountStatsConfigInput(in ports.SiteAccountStatsConfigInput) bool {
	return indexer.ValidSiteAccountID(in.ExpectedAccountID) && in.Profile == ports.NexusPHPClassicAccountStatsProfile && in.IntervalSeconds >= 900 && in.IntervalSeconds <= 86400 && in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 && in.ExpectedSourceVersion >= 1
}

func accountStatsConfigKey(key string) bool {
	if len(key) < 1 || len(key) > 160 {
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

func accountStatsConfigValid(c ports.SiteAccountStatsConfig, source uuid.UUID) bool {
	return c.IndexerID == source && c.Version > 0 && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt) && accountStatsConfigInput(ports.SiteAccountStatsConfigInput{ExpectedAccountID: c.ExpectedAccountID, Profile: c.Profile, Enabled: c.Enabled, IntervalSeconds: c.IntervalSeconds, ExpectedVersion: c.Version - 1, ExpectedSourceVersion: c.SourceConfigVersion})
}

func accountStatsConfigError(err error) error {
	for _, safe := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrRejected, ports.ErrNotFound, ports.ErrEndpointVersionConflict} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}

func (s *AccountStatsConfigService) Read(ctx context.Context, actor *ports.Actor, source uuid.UUID) (*ports.SiteAccountStatsConfig, error) {
	a, err := accountStatsConfigActor(ctx, actor, source)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	c, err := s.repo.ReadSiteAccountStatsConfig(ctx, &a, source)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if c == nil {
		return nil, nil
	}
	if !accountStatsConfigValid(*c, source) {
		return nil, ports.ErrUnavailable
	}
	out := *c
	return &out, nil
}

func (s *AccountStatsConfigService) Save(ctx context.Context, actor *ports.Actor, source uuid.UUID, in ports.SiteAccountStatsConfigInput, key string) (*ports.SiteAccountStatsConfigReceipt, error) {
	a, err := accountStatsConfigActor(ctx, actor, source)
	if err != nil {
		return nil, err
	}
	if !accountStatsConfigInput(in) || !accountStatsConfigKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	r, err := s.repo.SaveSiteAccountStatsConfig(ctx, &a, source, in, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if r == nil || r.CommandKey != key || !accountStatsConfigValid(r.Config, source) || r.Config.Version != in.ExpectedVersion+1 || r.Config.SourceConfigVersion != in.ExpectedSourceVersion || r.Config.ExpectedAccountID != in.ExpectedAccountID || r.Config.Profile != in.Profile || r.Config.Enabled != in.Enabled || r.Config.IntervalSeconds != in.IntervalSeconds {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

// Receipt never resubmits the original command or performs source I/O.
func (s *AccountStatsConfigService) Receipt(ctx context.Context, actor *ports.Actor, source uuid.UUID, key string) (*ports.SiteAccountStatsConfigReceipt, error) {
	a, err := accountStatsConfigActor(ctx, actor, source)
	if err != nil {
		return nil, err
	}
	if !accountStatsConfigKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	r, err := s.repo.ReadSiteAccountStatsConfigReceipt(ctx, &a, source, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if r == nil || r.CommandKey != key || !accountStatsConfigValid(r.Config, source) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}
