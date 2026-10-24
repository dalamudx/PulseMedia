package app

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type movieWatchProbe struct {
	complete     atomic.Bool
	partials     atomic.Int32
	active       atomic.Int32
	firstPartial atomic.Int64
	lastPartial  atomic.Int64
	triggerAt    atomic.Int64
	completeAt   atomic.Int64
	importAt     atomic.Int64
	lastReason   atomic.Value
}

// Keep all older pipeline scenarios on their original behavior.
func runFullMoviePipelineSource(t *testing.T, selected bool, mappingMode, directoryMode string, beforeStart ...func(*organizationFixture, *ports.Actor, uuid.UUID, *App) func()) {
	runFullMoviePipelineMode(t, selected, mappingMode, directoryMode, false, beforeStart...)
}

func TestAppRunDownloadWatchTriggersMovieImport(t *testing.T) {
	runFullMoviePipelineMode(t, false, "", "", true)
}

func triggerMovieWatch(t *testing.T, ctx context.Context, f *organizationFixture, source string, payload []byte, probe *movieWatchProbe) {
	t.Helper()
	defer func() {
		if !t.Failed() {
			return
		}
		diagnosticCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		page, err := postgres.NewDownloadWatchStore(f.pool).ListDownloadWatchSources(diagnosticCtx, ports.DownloadWatchSourceQuery{Limit: 64})
		t.Logf("watch readiness partials=%d active=%d sources=%d issues=%v read_failed=%t triggered=%t", probe.partials.Load(), probe.active.Load(), len(page.Sources), page.Issues, err != nil, probe.triggerAt.Load() != 0)
		var progress float64
		_ = f.pool.QueryRow(diagnosticCtx, `SELECT coalesce(max(progress),0) FROM download_task`).Scan(&progress)
		t.Logf("watch readiness stored_progress=%g controller_reason=%v", progress, probe.lastReason.Load())
		watchReadinessDiagnostics(t, diagnosticCtx, f, page.Sources)
	}()
	waitOutboxCondition(t, ctx, func() bool {
		var count int
		e := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_task WHERE state='tracking' AND progress>0 AND progress<1`).Scan(&count)
		return e == nil && count == 1 && probe.partials.Load() >= 1 && probe.active.Load() == 0
	})
	// Let the native client budget replenish, leaving the 30s periodic gate closed.
	timer := time.NewTimer(time.Until(time.Unix(0, probe.lastPartial.Load()).Add(1100 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal("watch setup exceeded owned deadline")
	case <-timer.C:
	}
	if age := time.Since(time.Unix(0, probe.firstPartial.Load())); age >= 20*time.Second {
		t.Fatal("test no longer excludes ordinary 30s polling", age)
	}
	probe.triggerAt.Store(time.Now().UnixNano())
	probe.complete.Store(true)
	if e := os.WriteFile(source, payload, 0600); e != nil {
		t.Fatal(e)
	}
}

func assertMovieWatch(t *testing.T, probe *movieWatchProbe) {
	t.Helper()
	trigger, native, imported := probe.triggerAt.Load(), probe.completeAt.Load(), probe.importAt.Load()
	if trigger == 0 || native < trigger || imported < native {
		t.Fatal("missing actual event/observation/import evidence")
	}
	delay := time.Duration(native - trigger)
	if delay >= 2*time.Second {
		t.Fatal("event did not accelerate native observation", delay)
	}
	t.Logf("M8_WATCH_APP_PIPELINE_PASS real_App=true owned_native=true partial_before_event=true native_after_event_us=%d import_observed_ms=%d task=1 Add=1 inode_same=true periodic_30s_excluded=true", delay.Microseconds(), time.Duration(imported-trigger).Milliseconds())
}
