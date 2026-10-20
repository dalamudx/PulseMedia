package site

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const accountStatsRequestTimeout = 30 * time.Second

type AccountStatsCollector struct {
	repo    ports.SiteAccountStatsRepository
	builder ports.SiteAccountStatsClientBuilder
}

func NewAccountStatsCollector(repo ports.SiteAccountStatsRepository, builder ports.SiteAccountStatsClientBuilder) *AccountStatsCollector {
	return &AccountStatsCollector{repo: repo, builder: builder}
}

func accountStatsCollectionError(err error) error {
	if err == nil {
		return nil
	}
	if limit, ok := ports.IsRateLimited(err); ok {
		delay := limit.RetryAfter
		if delay <= 0 {
			delay = 10 * time.Second
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		return ports.NewRateLimitError(delay, "site account statistics rate limited")
	}
	for _, safe := range []error{ports.ErrAuthRequired, ports.ErrBlocked, ports.ErrBadResponse, ports.ErrRejected, ports.ErrTimeout, ports.ErrEndpointVersionConflict} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}

func cloneAccountStats(in *ports.SiteAccountStats) *ports.SiteAccountStats {
	if in == nil {
		return nil
	}
	out := *in
	if in.Uploaded != nil {
		x := *in.Uploaded
		out.Uploaded = &x
	}
	if in.Downloaded != nil {
		x := *in.Downloaded
		out.Downloaded = &x
	}
	if in.Bonus != nil {
		x := *in.Bonus
		out.Bonus = &x
	}
	return &out
}

// Execute keeps its physical source permit until the provider actually returns.
func (s *AccountStatsCollector) Execute(ctx context.Context, lease ports.JobLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.builder == nil {
		return ports.ErrUnavailable
	}
	permit, err := s.repo.Acquire(ctx, lease)
	if err != nil {
		return accountStatsCollectionError(err)
	}
	if permit == nil {
		return ports.ErrUnavailable
	}
	defer permit.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	frozen := permit.Snapshot()
	if !accountStatsConfigValid(frozen.Config, frozen.Config.IndexerID) || frozen.Config.IndexerID == uuid.Nil || !frozen.Config.Enabled || strings.TrimSpace(frozen.Cookie) == "" || frozen.SourceURL == "" {
		return ports.ErrRejected
	}
	client, err := s.builder.NewSiteAccountStatsClient(frozen)
	if err != nil {
		return permit.Finish(ctx, nil, accountStatsCollectionError(err))
	}
	if client == nil {
		return permit.Finish(ctx, nil, ports.ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err = permit.Reserve(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return permit.Finish(ctx, nil, accountStatsCollectionError(err))
	}
	if err = permit.Check(ctx); err != nil {
		return accountStatsCollectionError(err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, accountStatsRequestTimeout)
	value, readErr := client.ReadAccountStats(requestCtx, frozen.Config.ExpectedAccountID)
	requestErr := requestCtx.Err()
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if requestErr != nil {
		readErr = requestErr
	}
	if err = permit.Check(ctx); err != nil {
		return accountStatsCollectionError(err)
	}
	if readErr != nil {
		return permit.Finish(ctx, nil, accountStatsCollectionError(readErr))
	}
	if indexer.ValidateSiteAccountStats(value) != nil || value.AccountID != frozen.Config.ExpectedAccountID {
		return permit.Finish(ctx, nil, ports.ErrBadResponse)
	}
	return permit.Finish(ctx, cloneAccountStats(&value), nil)
}
