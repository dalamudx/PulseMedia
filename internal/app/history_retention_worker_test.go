package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

type blockedHistoryPruner struct {
	started  chan struct{}
	returned chan struct{}
}

func (p blockedHistoryPruner) PruneHistory(ctx context.Context) (postgres.HistoryPruneStats, error) {
	close(p.started)
	<-ctx.Done()
	close(p.returned)
	return postgres.HistoryPruneStats{}, ctx.Err()
}

func TestHistoryRetentionWorkerCancelsAndReturns(t *testing.T) {
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	repo := blockedHistoryPruner{make(chan struct{}), make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); a.runHistoryRetentionWorker(ctx, repo) }()
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not join canceled work")
	}
	select {
	case <-repo.returned:
	default:
		t.Fatal("worker returned before database work")
	}
}
