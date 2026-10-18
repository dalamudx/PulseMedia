package app

import (
	"bytes"
	"context"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type ownedStartupIO struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *ownedStartupIO) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("HTTP server listening")) {
		w.once.Do(func() { close(w.entered) })
		<-w.release
	}
	return len(p), nil
}

func TestExternalCloseRetainsDataLockUntilOwnedRuntimeIOEnds(t *testing.T) {
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	writer := &ownedStartupIO{entered: make(chan struct{}), release: make(chan struct{})}
	application, err := New(cfg, WithLogger(slog.New(slog.NewTextHandler(writer, nil))))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- application.Run(runCtx) }()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(writer.release) }) }
	defer unblock()
	select {
	case <-writer.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime never reached owned I/O")
	}
	if err := application.Run(context.Background()); err == nil {
		t.Error("second runtime started without a new owner")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- application.Close() }()
	releasedEarly := false
	deadline := time.NewTimer(200 * time.Millisecond)
	ticker := time.NewTicker(5 * time.Millisecond)
loop:
	for {
		select {
		case <-deadline.C:
			break loop
		case <-ticker.C:
			probe, err := lockfile.Acquire(cfg.DataDir)
			if err == nil {
				releasedEarly = true
				if err := probe.Release(); err != nil {
					t.Error(err)
				}
				break loop
			}
		}
	}
	ticker.Stop()
	deadline.Stop()
	if releasedEarly {
		t.Error("external Close released data lock before owned runtime I/O ended")
	}
	unblock()
	for _, done := range []<-chan error{closeDone, runDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("shutdown did not finish after owned I/O ended")
		}
	}
	probe, err := lockfile.Acquire(cfg.DataDir)
	if err != nil {
		t.Fatal("data lock not released after complete drain")
	}
	if err := probe.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestClosedAppCannotRunAndCloseIsConcurrentIdempotent(t *testing.T) {
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	application, err := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := application.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := application.Run(ctx); err == nil {
		t.Fatal("closed app started again without data-root ownership")
	}
}
