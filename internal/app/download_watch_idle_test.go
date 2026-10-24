package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestDownloadWatchIdleRefreshDoesNotPollDownloader(t *testing.T) {
	binding := watchBinding()
	repo := watchRepo(binding)
	var lists, resolves, observations atomic.Int32
	refreshed := make(chan struct{})
	observed := make(chan struct{})
	var once sync.Once
	repo.list = func(context.Context, ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
		if lists.Add(1) == 8 {
			close(refreshed)
		}
		return ports.DownloadWatchSourcePage{Sources: []ports.DownloadWatchSource{{Binding: binding}}, Complete: true}, nil
	}
	repo.resolve = func(context.Context, ports.DownloadWatchBinding, ports.RootChangeHint, ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
		resolves.Add(1)
		return ports.DownloadWatchTargetPage{Targets: []ports.DownloadWatchTarget{watchTarget(binding)}, Complete: true}, nil
	}
	observer := watchObserverFake(func(context.Context, ports.DownloadWatchTarget) (*ports.DownloadExecutionTask, error) {
		observations.Add(1)
		once.Do(func() { close(observed) })
		return &ports.DownloadExecutionTask{State: "tracking"}, nil
	})
	watchStart(t, repo, watchFS(), observer, downloadWatchOptions{refresh: 5 * time.Millisecond})
	watchReceive(t, observed)
	watchReceive(t, refreshed)
	if observations.Load() != 1 || resolves.Load() != 1 {
		t.Fatal("idle configuration refresh produced extra native observations", observations.Load(), resolves.Load())
	}
}
