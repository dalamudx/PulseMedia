package postgres

import (
	"context"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const movieUpgradeWatchTTL = 40 * time.Second

// Immutable after issuance: concurrent callers share no mutable scheduling state.
type movieUpgradeWatchCapture struct {
	request  ports.MovieUpgradeTrackingHintRequest
	store    *MovieUpgradeWatchStore
	hint     upgradeTrackingHint
	digest   string
	deadline time.Time
	due      time.Time
}

func (c *movieUpgradeWatchCapture) MovieUpgradeTrackingHintRequest() ports.MovieUpgradeTrackingHintRequest {
	return c.request
}

// The periodic path retains its original transaction and admission behavior.
func (r *MovieUpgradeTrackingRuntimeStore) scheduleOne(ctx context.Context, h upgradeTrackingHint) (bool, error) {
	return r.scheduleOneWatched(ctx, h, nil)
}

func (r *MovieUpgradeTrackingRuntimeStore) lockMovieUpgradeWatchIsolation(ctx context.Context, tx pgx.Tx, c *movieUpgradeWatchCapture) error {
	if c == nil {
		return nil
	}
	if c.store == nil || c.store.runtime != r {
		return ports.ErrDownloadWatchStale
	}
	return watchGuard(ctx, db.New(tx))
}

// Called after the original business locks, and again after all deferred effects.
func (r *MovieUpgradeTrackingRuntimeStore) validateMovieUpgradeWatchCapture(ctx context.Context, tx pgx.Tx, c *movieUpgradeWatchCapture, own uuid.UUID) error {
	if c == nil {
		return nil
	}
	if c.store == nil || c.store.runtime != r {
		return ports.ErrDownloadWatchStale
	}
	if _, err := c.store.remaining(c.request); err != nil {
		return err
	}
	digest, due, err := c.store.facts(ctx, tx, c.request, own)
	if err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if digest != c.digest || !due.Equal(c.due) || !now.Before(c.deadline) {
		return ports.ErrDownloadWatchStale
	}
	if own != uuid.Nil && now.Before(c.due) {
		return ports.ErrDownloadWatchStale
	}
	return nil
}
