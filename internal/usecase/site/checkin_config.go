package site

import (
	"context"
	"math"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type CheckinConfigService struct {
	repo ports.SiteCheckinConfigRepository
}

func NewCheckinConfigService(repo ports.SiteCheckinConfigRepository) *CheckinConfigService {
	return &CheckinConfigService{repo: repo}
}

func checkinConfigInput(in ports.SiteCheckinConfigInput) bool {
	return indexer.ValidSiteAccountID(in.ExpectedAccountID) && in.Profile == ports.NexusPHP1103CheckinProfile && indexer.ValidSiteCheckinSchedule(in.Timezone, in.DailyAt) && in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 && in.ExpectedSourceVersion >= 1
}

func checkinConfigValid(c ports.SiteCheckinConfig, source uuid.UUID) bool {
	return c.IndexerID == source && c.Version > 0 && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt) && checkinConfigInput(ports.SiteCheckinConfigInput{ExpectedAccountID: c.ExpectedAccountID, Profile: c.Profile, Timezone: c.Timezone, DailyAt: c.DailyAt, Enabled: c.Enabled, ExpectedVersion: c.Version - 1, ExpectedSourceVersion: c.SourceConfigVersion})
}

func (s *CheckinConfigService) Read(ctx context.Context, actor *ports.Actor, source uuid.UUID) (*ports.SiteCheckinConfig, error) {
	a, err := accountStatsConfigActor(ctx, actor, source)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	c, err := s.repo.ReadSiteCheckinConfig(ctx, &a, source)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if c == nil {
		return nil, nil
	}
	if !checkinConfigValid(*c, source) {
		return nil, ports.ErrUnavailable
	}
	out := *c
	return &out, nil
}

func (s *CheckinConfigService) Save(ctx context.Context, actor *ports.Actor, source uuid.UUID, in ports.SiteCheckinConfigInput, key string) (*ports.SiteCheckinConfigReceipt, error) {
	a, err := accountStatsConfigActor(ctx, actor, source)
	if err != nil {
		return nil, err
	}
	if !checkinConfigInput(in) || !accountStatsConfigKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	r, err := s.repo.SaveSiteCheckinConfig(ctx, &a, source, in, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if r == nil || r.CommandKey != key || !checkinConfigValid(r.Config, source) || r.Config.Version != in.ExpectedVersion+1 || r.Config.SourceConfigVersion != in.ExpectedSourceVersion || r.Config.ExpectedAccountID != in.ExpectedAccountID || r.Config.Profile != in.Profile || r.Config.Enabled != in.Enabled || r.Config.Timezone != in.Timezone || r.Config.DailyAt != in.DailyAt {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

// Receipt reads a historical result; it never observes or submits attendance.
func (s *CheckinConfigService) Receipt(ctx context.Context, actor *ports.Actor, source uuid.UUID, key string) (*ports.SiteCheckinConfigReceipt, error) {
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
	r, err := s.repo.ReadSiteCheckinConfigReceipt(ctx, &a, source, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if r == nil || r.CommandKey != key || !checkinConfigValid(r.Config, source) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}
