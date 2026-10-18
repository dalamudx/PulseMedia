package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestAppLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pulsemedia-app-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	dataDir := filepath.Join(tempDir, "data")
	cfg := config.NewDefault()
	cfg.DataDir = dataDir
	cfg.Server.HTTPAddr = "127.0.0.1:0"

	var logBuf bytes.Buffer
	logger := logging.NewLogger(&logBuf, "debug")
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	mockIDGen := idgen.New()

	app1, err := New(cfg,
		WithLogger(logger),
		WithClock(mockClock),
		WithIDGen(mockIDGen),
		WithVersion("1.0.0-test"),
	)
	if err != nil {
		t.Fatalf("failed to initialize app1: %v", err)
	}

	// 1. Healthz & Readyz verification
	h := app1.Healthz()
	if !h.Alive || h.Version != "1.0.0-test" {
		t.Fatalf("unexpected healthz: %+v", h)
	}

	ctx := context.Background()
	r := app1.Readyz(ctx)
	if !r.Ready || !r.Checks["datadir"] {
		t.Fatalf("expected app1 to be ready, got: %+v", r)
	}

	// Test check failure propagation
	app1.SetCheck("component_x", false, "waiting for component_x")
	r = app1.Readyz(ctx)
	if r.Ready || r.Checks["component_x"] || r.Reason != "waiting for component_x" {
		t.Fatalf("expected unready state when check fails: %+v", r)
	}

	app1.SetCheck("component_x", true, "waiting for component_x")
	r = app1.Readyz(ctx)
	if !r.Ready || !r.Checks["component_x"] {
		t.Fatalf("expected ready state after check ok: %+v", r)
	}

	// 2. Dual-launch prevention on same data directory
	_, err = New(cfg)
	if err == nil {
		t.Fatal("expected second app instance on same dataDir to fail")
	}
	if !errors.Is(err, lockfile.ErrAlreadyLocked) {
		t.Fatalf("expected ErrAlreadyLocked error, got: %v", err)
	}

	// 3. Graceful shutdown releases lock
	runCtx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- app1.Run(runCtx)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case runErr := <-runErrCh:
		if runErr != nil {
			t.Fatalf("unexpected error during graceful shutdown: %v", runErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for app shutdown")
	}

	// 4. Re-initialization after shutdown should now succeed
	app2, err := New(cfg, WithLogger(logger))
	if err != nil {
		t.Fatalf("expected successful re-initialization after shutdown, got: %v", err)
	}
	defer func() {
		_ = app2.Close()
	}()
}

func TestAppDatabaseReadiness(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	tempDir, err := os.MkdirTemp("", "pulsemedia-app-dbtest-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	cfg := config.NewDefault()
	cfg.DataDir = filepath.Join(tempDir, "data")
	cfg.Database.URL = dbDSN
	cfg.Server.HTTPAddr = "127.0.0.1:0"

	ctx := context.Background()

	// 1. App initialized against unmigrated database: Readyz should fail
	appUnmigrated, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to initialize app: %v", err)
	}

	readyStatus := appUnmigrated.Readyz(ctx)
	if readyStatus.Ready || readyStatus.Checks["database"] {
		t.Fatalf("expected unready state on unmigrated database: %+v", readyStatus)
	}
	if err := appUnmigrated.Close(); err != nil {
		t.Fatalf("failed to close app: %v", err)
	}

	// 2. Run migrations
	if _, err := postgres.Migrate(ctx, dbDSN, "../../migrations"); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	// 3. App re-initialized against migrated database: Readyz should be ready
	appMigrated, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to initialize app with migrated db: %v", err)
	}
	defer func() {
		_ = appMigrated.Close()
	}()

	readyStatus = appMigrated.Readyz(ctx)
	if !readyStatus.Ready || !readyStatus.Checks["database"] {
		t.Fatalf("expected ready state on migrated database: %+v", readyStatus)
	}
	if appMigrated.Queries() == nil {
		t.Fatal("expected non-nil Queries() when database is configured")
	}
}
