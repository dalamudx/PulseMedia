package site

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type AccountStatsReader struct {
	repo ports.SiteAccountStatsRepository
}

func NewAccountStatsReader(repo ports.SiteAccountStatsRepository) *AccountStatsReader {
	return &AccountStatsReader{repo: repo}
}

// Read observes persisted data only; it never starts collection or contacts a site.
func (s *AccountStatsReader) Read(ctx context.Context, actor *ports.Actor, id uuid.UUID) (*ports.SiteAccountStatsView, error) {
	a, err := accountStatsConfigActor(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	view, err := s.repo.ReadSiteAccountStats(ctx, &a, id)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, accountStatsConfigError(err)
	}
	if view == nil {
		return nil, nil
	}
	if !accountStatsConfigValid(view.Config, id) {
		return nil, ports.ErrUnavailable
	}
	switch view.CollectionState {
	case "never_collected", "queued", "running", "succeeded", "failed", "review_required", "disabled":
	default:
		return nil, ports.ErrUnavailable
	}
	switch view.FailureCode {
	case "", "auth_required", "blocked", "rate_limited", "bad_response", "unavailable", "interrupted":
	default:
		return nil, ports.ErrUnavailable
	}
	if view.Stats == nil {
		if view.ObservedAt != nil || view.SampleConfigVersion != 0 || view.SampleSourceVersion != 0 || !view.Stale {
			return nil, ports.ErrUnavailable
		}
	} else if view.ObservedAt == nil || view.ObservedAt.IsZero() || view.SampleConfigVersion < 1 || view.SampleSourceVersion < 1 || view.SampleConfigVersion > view.Config.Version || view.SampleSourceVersion > view.Config.SourceConfigVersion || indexer.ValidateSiteAccountStats(*view.Stats) != nil || view.Stats.AccountID != view.Config.ExpectedAccountID {
		return nil, ports.ErrUnavailable
	}
	if view.LastAttemptAt != nil && view.LastAttemptAt.IsZero() {
		return nil, ports.ErrUnavailable
	}
	out := *view
	out.Stats = cloneAccountStats(view.Stats)
	cloneTime := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		copy := *t
		return &copy
	}
	out.ObservedAt = cloneTime(view.ObservedAt)
	out.LastAttemptAt = cloneTime(view.LastAttemptAt)
	return &out, nil
}
