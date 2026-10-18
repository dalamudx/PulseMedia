package transfer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

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

func TestUpgradeMediaFileAtomicSwitchAndFactRevocation(t *testing.T) {
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
	defer pool.Close()

	gen := idgen.New()
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	ob := outbox.New(pool, gen, mockClock)
	transferSvc := transfer.NewService(pool, gen, ob, rootfs.NewFilesystem())
	queries := postgres.Queries(pool)

	tempDir, err := os.MkdirTemp("", "pulsemedia-upgrade-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	moviesDir := filepath.Join(tempDir, "movies")
	downloadsDir := filepath.Join(tempDir, "downloads")
	_ = os.MkdirAll(moviesDir, 0755)
	if err := os.WriteFile(filepath.Join(moviesDir, ".pulsemedia_root"), []byte("fixture root"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(downloadsDir, 0755)

	// The source directory is explicitly configured; arbitrary /tmp paths are not capabilities.
	if _, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: downloadsDir, MediaType: "movie", Name: "Downloads"}); err != nil {
		t.Fatalf("configure source root: %v", err)
	}

	rootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      moviesDir,
		MediaType: "movie",
		Name:      "Movies",
	})
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Fight Club", MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})

	// 1. Initial 1080p file exists in library
	oldFileID, _ := uuid.NewV7()
	oldRelPath := "Fight Club (1999)/Fight Club (1999) [WEB-DL 1080p].mkv"
	oldFile, err := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              oldFileID,
		RootID:          rootID,
		RelPath:         oldRelPath,
		SizeBytes:       5000000000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	if err != nil {
		t.Fatalf("create old file failed: %v", err)
	}
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: oldFile.ID, UnitID: unitID})

	// Add imported and confirmed facts to old file
	fact1ID, _ := uuid.NewV7()
	_, _ = queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             fact1ID,
		UnitID:         unitID,
		Stage:          "imported",
		SourceFileID:   pgtype.UUID{Bytes: oldFile.ID, Valid: true},
		RootID:         pgtype.UUID{Bytes: rootID, Valid: true},
		QualityProfile: []byte(`{"resolution":"1080p"}`),
	})
	fact2ID, _ := uuid.NewV7()
	_, _ = queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             fact2ID,
		UnitID:         unitID,
		Stage:          "confirmed",
		SourceFileID:   pgtype.UUID{Bytes: oldFile.ID, Valid: true},
		RootID:         pgtype.UUID{Bytes: rootID, Valid: true},
		QualityProfile: []byte(`{"resolution":"1080p"}`),
	})

	// 2. Prepare new 4K Remux downloaded file on disk
	newDownloadedPath := filepath.Join(downloadsDir, "Fight.Club.1999.2160p.UHD.Remux.mkv")
	_ = os.WriteFile(newDownloadedPath, make([]byte, 1024), 0644)

	newQuality := quality.Profile{
		Resolution: quality.Res2160p,
		Source:     quality.SourceRemux,
	}

	// 3. Execute UpgradeMediaFile
	opID, _ := uuid.NewV7()
	err = transferSvc.UpgradeMediaFile(ctx, opID, rootID, unitID, oldFile.ID, newDownloadedPath, "Fight Club", 1999, newQuality)
	if err != nil {
		t.Fatalf("upgrade media file failed: %v", err)
	}

	// 4. Verify Invariant: Old file is RETIRED
	retiredOldFile, err := queries.GetFileByID(ctx, oldFile.ID)
	if err != nil || !retiredOldFile.RetiredAt.Valid {
		t.Fatalf("expected old file to be retired: %+v", retiredOldFile)
	}

	// 5. Verify Invariant: Historical confirmed/imported facts on old file are REVOKED (历史确认失效!)
	activeOldFacts, _ := queries.ListActiveFactsForUnit(ctx, unitID)
	for _, f := range activeOldFacts {
		if f.SourceFileID.Valid && f.SourceFileID.Bytes == oldFile.ID {
			t.Fatalf("historical fact on retired file should be revoked: %+v", f)
		}
	}

	// 6. Verify Invariant: New file has active imported fact
	newFiles, err := queries.ListFilesForUnit(ctx, unitID)
	if err != nil || len(newFiles) != 1 {
		t.Fatalf("expected 1 active file for unit: %d files found", len(newFiles))
	}
	if newFiles[0].ID == oldFile.ID {
		t.Fatal("active file should not be the old file")
	}

	newActiveFacts, _ := queries.ListActiveFactsForUnit(ctx, unitID)
	if len(newActiveFacts) != 1 || newActiveFacts[0].Stage != "imported" {
		t.Fatalf("expected new active imported fact for unit: %+v", newActiveFacts)
	}
}
