package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *SiteAccountStatsStore) ReadSiteAccountStats(ctx context.Context, actor *ports.Actor, id uuid.UUID) (out *ports.SiteAccountStatsView, err error) {
	defer func() {
		err = statsError(err)
		if err != nil {
			out = nil
		}
	}()
	a, err := statsActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	sourceVersion, err := q.GetSiteAccountStatsReadSource(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	row, err := q.GetSiteAccountStatsConfig(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = statsGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	config, err := statsSnapshot(ctx, q, row)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	available, err := IndexerAvailableForExecution(ctx, q, id)
	if err != nil {
		return nil, err
	}
	out = &ports.SiteAccountStatsView{Config: *config, CollectionState: "never_collected", Stale: true}
	attempt, err := q.GetSiteAccountStatsLatestAttempt(ctx, id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		switch attempt.State {
		case "queued", "running", "succeeded", "failed", "review_required":
			out.CollectionState = attempt.State
		default:
			return nil, ports.ErrUnavailable
		}
		switch attempt.FailureCode {
		case "", "auth_required", "blocked", "rate_limited", "bad_response", "unavailable", "interrupted":
			out.FailureCode = attempt.FailureCode
		default:
			return nil, ports.ErrUnavailable
		}
		if attempt.StartedAt.Valid {
			at := attempt.StartedAt.Time
			out.LastAttemptAt = &at
		}
	}
	if !config.Enabled {
		out.CollectionState = "disabled"
	}
	sample, err := q.GetSiteAccountStatsLatestSuccess(ctx, id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		var stats ports.SiteAccountStats
		var fields map[string]any
		if statsDecode(sample.Sample, &fields) != nil || len(fields) != 5 {
			return nil, ports.ErrUnavailable
		}
		for _, key := range []string{"account_id", "uploaded", "downloaded", "ratio", "bonus"} {
			if _, ok := fields[key]; !ok {
				return nil, ports.ErrUnavailable
			}
		}
		if statsDecode(sample.Sample, &stats) != nil {
			return nil, ports.ErrUnavailable
		}
		if stats.AccountID == config.ExpectedAccountID {
			if indexer.ValidateSiteAccountStats(stats) != nil || !sample.FinishedAt.Valid || sample.ConfigVersion < 1 || sample.SourceVersion < 1 {
				return nil, ports.ErrUnavailable
			}
			now, e := q.GetSiteAccountStatsReadClock(ctx)
			if e != nil || !now.Valid || sample.FinishedAt.Time.After(now.Time) {
				return nil, ports.ErrUnavailable
			}
			out.Stats = &stats
			at := sample.FinishedAt.Time
			out.ObservedAt = &at
			out.SampleConfigVersion = sample.ConfigVersion
			out.SampleSourceVersion = sample.SourceVersion
			out.Stale = !config.Enabled || !available || sourceVersion != config.SourceConfigVersion || sample.ConfigVersion != config.Version || sample.SourceVersion != sourceVersion || !now.Time.Before(at.Add(time.Duration(config.IntervalSeconds)*time.Second)) || out.CollectionState == "failed" || out.CollectionState == "review_required"
		} else if !indexer.ValidSiteAccountID(stats.AccountID) {
			return nil, ports.ErrUnavailable
		}
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
