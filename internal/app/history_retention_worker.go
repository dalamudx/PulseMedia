package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

type historyRetentionPruner interface {
	PruneHistory(context.Context) (postgres.HistoryPruneStats, error)
}

// App cancels and joins this loop. Each tick is one bounded, network-free UoW.
func (a *App) runHistoryRetentionWorker(ctx context.Context, repo historyRetentionPruner) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		stats, err := repo.PruneHistory(work)
		cancel()
		if err != nil && ctx.Err() == nil {
			a.logger.Error("history retention unavailable")
		}
		if err == nil && stats.Observations+stats.Jobs+stats.Candidates > 0 {
			a.logger.Info("expired observations pruned", slog.Int64("observations", stats.Observations), slog.Int64("jobs", stats.Jobs), slog.Int64("candidates", stats.Candidates))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
