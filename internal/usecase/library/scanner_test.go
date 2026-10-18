package library_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
)

const migrationsDir = "../../../migrations"

func setupScannerTest(t *testing.T) (*library.Scanner, *db.Queries, string) {
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
	scanner := library.NewScanner(gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(pool))
	queries := postgres.Queries(pool)
	return scanner, queries, dbDSN
}

func TestScannerMarkerAndOfflineProtection(t *testing.T) {
	scanner, queries, _ := setupScannerTest(t)
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "pulsemedia-scanner-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	rootPath := filepath.Join(tempDir, "movies")
	_ = os.MkdirAll(rootPath, 0755)

	rootID, _ := uuid.NewV7()
	root, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      rootPath,
		MediaType: "movie",
		Name:      "Movies",
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}

	// 1. Scan directory WITHOUT marker file must fail with ErrMarkerMissing!
	_, err = scanner.ScanRoot(ctx, root.ID)
	if !errors.Is(err, library.ErrMarkerMissing) {
		t.Fatalf("expected ErrMarkerMissing for directory without marker, got: %v", err)
	}

	// Verify root health changed to offline
	updatedRoot, _ := queries.GetLibraryRootByID(ctx, root.ID)
	if updatedRoot.Health != "offline" {
		t.Fatalf("expected root to be marked offline when marker missing, got: %s", updatedRoot.Health)
	}

	// 2. Create marker file (.pulsemedia_root) and movie files
	markerFile := filepath.Join(rootPath, ".pulsemedia_root")
	_ = os.WriteFile(markerFile, []byte("root_id"), 0644)

	movieDir := filepath.Join(rootPath, "Fight Club (1999)")
	_ = os.MkdirAll(movieDir, 0755)
	movieFile := filepath.Join(movieDir, "Fight Club (1999) [1080p].mkv")
	_ = os.WriteFile(movieFile, make([]byte, 1024), 0644)

	// 3. Scan now succeeds, discovers movie file, sets baseline completed!
	report, err := scanner.ScanRoot(ctx, root.ID)
	if err != nil {
		t.Fatalf("expected scan to succeed: %v", err)
	}
	if report.FilesObserved != 1 || report.FilesAdded != 1 {
		t.Fatalf("expected 1 file observed and added: %+v", report)
	}

	updatedRoot, _ = queries.GetLibraryRootByID(ctx, root.ID)
	if !updatedRoot.BaselineCompleted || updatedRoot.Health != "online" {
		t.Fatalf("expected baseline completed and online: %+v", updatedRoot)
	}

	// Verify file was added to library_file
	file, err := queries.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{
		RootID:  root.ID,
		RelPath: "Fight Club (1999)/Fight Club (1999) [1080p].mkv",
	})
	if err != nil || file.SizeBytes != 1024 {
		t.Fatalf("file not properly registered: %v", err)
	}
}

func TestScannerMutationSeqGate(t *testing.T) {
	_, queries, _ := setupScannerTest(t)
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "pulsemedia-mutation-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	rootPath := filepath.Join(tempDir, "movies")
	_ = os.MkdirAll(rootPath, 0755)
	_ = os.WriteFile(filepath.Join(rootPath, ".pulsemedia_root"), []byte("ok"), 0644)

	rootID, _ := uuid.NewV7()
	root, _ := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      rootPath,
		MediaType: "movie",
		Name:      "Movies",
	})

	// 1. Initial scan starts when root.mutation_seq = 0
	scanRunID, _ := uuid.NewV7()
	run, err := queries.StartScanRun(ctx, db.StartScanRunParams{
		ID:   root.ID,
		ID_2: scanRunID,
	})
	if err != nil {
		t.Fatalf("failed to start scan run: %v", err)
	}
	if run.StartMutationSeq != 0 {
		t.Fatalf("expected start_mutation_seq = 0, got %d", run.StartMutationSeq)
	}

	// 2. WHILE scan is in flight, a concurrent process writes a new file (e.g. transfer finishing)
	// It increments root.mutation_seq and inserts the new file with the higher mutation_seq
	newSeq, err := queries.IncrementRootMutationSeq(ctx, root.ID)
	if err != nil {
		t.Fatalf("failed to increment mutation seq: %v", err)
	}

	freshFileID, _ := uuid.NewV7()
	_, err = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              freshFileID,
		RootID:          root.ID,
		RelPath:         "FreshImport.mkv",
		SizeBytes:       2000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: newSeq,
	})
	if err != nil {
		t.Fatalf("failed to insert fresh file: %v", err)
	}

	// 3. Scan completes its walk and reconciles using its start_mutation_seq (0)
	// Files with last_mutation_seq > start_mutation_seq must NOT be checked for missing!
	reconcileFiles, err := queries.GetActiveFilesForReconcile(ctx, db.GetActiveFilesForReconcileParams{
		RootID:          root.ID,
		LastMutationSeq: run.StartMutationSeq,
	})
	if err != nil {
		t.Fatalf("reconcile query failed: %v", err)
	}

	for _, f := range reconcileFiles {
		if f.ID == freshFileID {
			t.Fatalf("mutation seq gate violated: fresh file with seq %d was included in reconcile for scan at seq %d",
				newSeq, run.StartMutationSeq)
		}
	}
}
