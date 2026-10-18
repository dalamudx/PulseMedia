package transfer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/store/transfer"
)

const migrationsDir = "../../../migrations"

func setupTransferTest(t *testing.T) (*transfer.Service, *db.Queries, uuid.UUID, uuid.UUID, string, string) {
	t.Helper()
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
	})

	gen := idgen.New()
	c := clock.New()
	ob := outbox.New(pool, gen, c)
	svc := transfer.NewService(pool, gen, ob, rootfs.NewFilesystem())
	queries := postgres.Queries(pool)

	tempDir, err := os.MkdirTemp("", "pulsemedia-transfer-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	downloadsDir := filepath.Join(tempDir, "downloads")
	moviesDir := filepath.Join(tempDir, "movies")
	_ = os.MkdirAll(downloadsDir, 0755)
	_ = os.MkdirAll(moviesDir, 0755)
	if err := os.WriteFile(filepath.Join(moviesDir, ".pulsemedia_root"), []byte("fixture root"), 0600); err != nil {
		t.Fatal(err)
	}

	// The source directory is explicitly configured; arbitrary /tmp paths are not capabilities.
	if _, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: downloadsDir, MediaType: "movie", Name: "Downloads"}); err != nil {
		t.Fatalf("configure source root: %v", err)
	}

	rootID, _ := uuid.NewV7()
	_, err = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      moviesDir,
		MediaType: "movie",
		Name:      "Movies",
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}

	// Create media & unit
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        mediaID,
		Title:     "Fight Club",
		MediaType: "movie",
	})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{
		ID:      unitID,
		MediaID: mediaID,
		UnitKey: "movie",
	})

	return svc, queries, rootID, unitID, downloadsDir, moviesDir
}

func TestTransferHardlinkExecutionAndObservationRecovery(t *testing.T) {
	svc, queries, rootID, unitID, downloadsDir, _ := setupTransferTest(t)
	ctx := context.Background()

	// Create dummy downloaded file
	sourceFile := filepath.Join(downloadsDir, "Fight.Club.1999.1080p.mkv")
	content := []byte("dummy movie video payload content 123456789")
	if err := os.WriteFile(sourceFile, content, 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	opID, _ := uuid.NewV7()
	q := quality.Profile{
		Resolution: quality.Res1080p,
		Source:     quality.SourceBluray,
	}

	// 1. Create Movie Plan
	plan, step, err := svc.CreateMoviePlan(ctx, opID, rootID, unitID, sourceFile, "Fight Club", 1999, q)
	if err != nil {
		t.Fatalf("create movie plan failed: %v", err)
	}
	if plan.State != "approved" || step.State != "pending" {
		t.Fatalf("unexpected initial plan/step state: plan=%s, step=%s", plan.State, step.State)
	}

	// 2. Execute Plan: hardlink created, post-condition verified, library file & imported fact created
	if err := svc.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatalf("execute plan failed: %v", err)
	}

	// Verify hardlink exists on disk and shares same inode
	srcInfo, err := os.Stat(sourceFile)
	if err != nil {
		t.Fatalf("failed to stat source: %v", err)
	}
	tgtInfo, err := os.Stat(step.TargetPath)
	if err != nil {
		t.Fatalf("failed to stat target: %v", err)
	}

	srcStat := srcInfo.Sys().(*syscall.Stat_t)
	tgtStat := tgtInfo.Sys().(*syscall.Stat_t)
	if srcStat.Dev != tgtStat.Dev || srcStat.Ino != tgtStat.Ino {
		t.Fatalf("hardlink failed: inode/device mismatch (src=%d/%d, tgt=%d/%d)",
			srcStat.Dev, srcStat.Ino, tgtStat.Dev, tgtStat.Ino)
	}

	// Verify imported fact in database
	facts, err := queries.ListActiveFactsForUnit(ctx, unitID)
	if err != nil || len(facts) != 1 {
		t.Fatalf("expected 1 imported fact, got %d (err: %v)", len(facts), err)
	}
	if facts[0].Stage != "imported" {
		t.Fatalf("expected imported stage, got: %s", facts[0].Stage)
	}

	// 3. Crash / Interruption observation recovery: executing an already linked plan is idempotent!
	if err := svc.ExecutePlan(ctx, plan.ID); err != nil {
		t.Fatalf("subsequent plan execution should succeed cleanly via observation recovery: %v", err)
	}
}

func TestTransferSymlinkRejection(t *testing.T) {
	svc, _, rootID, unitID, downloadsDir, _ := setupTransferTest(t)
	ctx := context.Background()

	// Create real file and symlink pointing to it
	realFile := filepath.Join(downloadsDir, "real.mkv")
	_ = os.WriteFile(realFile, []byte("data"), 0644)

	symlinkFile := filepath.Join(downloadsDir, "symlink.mkv")
	_ = os.Symlink(realFile, symlinkFile)

	opID, _ := uuid.NewV7()
	q := quality.Profile{}

	// Attempt to create plan with symlink source: must fail with ErrSymlinkForbidden!
	_, _, err := svc.CreateMoviePlan(ctx, opID, rootID, unitID, symlinkFile, "Movie", 2020, q)
	if !errors.Is(err, transfer.ErrSymlinkForbidden) {
		t.Fatalf("expected ErrSymlinkForbidden for symlink source, got: %v", err)
	}
}
