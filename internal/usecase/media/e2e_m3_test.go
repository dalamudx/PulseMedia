package media_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	legacytransfer "github.com/dalamudx/PulseMedia/internal/adapter/transferlegacy"
	domainMedia "github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	libraryreuse "github.com/dalamudx/PulseMedia/internal/store/libraryreuse"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
)

func TestFullM3ScenariosIntegration(t *testing.T) {
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
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}

	queries := postgres.Queries(pool)

	tempDir, err := os.MkdirTemp("", "pulsemedia-e2e-m3-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	tvDir := filepath.Join(tempDir, "tv_primary")
	moviesDir := filepath.Join(tempDir, "movies_primary")
	archiveDir := filepath.Join(tempDir, "archive")
	_ = os.MkdirAll(tvDir, 0755)
	_ = os.MkdirAll(moviesDir, 0755)
	_ = os.MkdirAll(archiveDir, 0755)

	_ = os.WriteFile(filepath.Join(tvDir, ".pulsemedia_root"), []byte("tv"), 0644)
	_ = os.WriteFile(filepath.Join(moviesDir, ".pulsemedia_root"), []byte("mov"), 0644)
	_ = os.WriteFile(filepath.Join(archiveDir, ".pulsemedia_root"), []byte("arc"), 0644)

	// Create Admin User
	authSvc := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, mockClock, gen)
	adminUser, _, err := authSvc.SetupAdmin(ctx, "admin", "AdminM3Pass12345!")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: adminUser.ID.String(), Name: adminUser.Username, Role: ports.RoleAdmin}

	// Create Roots
	tvRootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: tvRootID, Path: tvDir, MediaType: "tv", Name: "TV Primary"})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", tvRootID)

	movRootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: movRootID, Path: moviesDir, MediaType: "movie", Name: "Movies Primary"})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", movRootID)

	arcRootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: arcRootID, Path: archiveDir, MediaType: "movie", Name: "Archive"})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", arcRootID)

	// 1. Scenario: Multi-Episode File Registration & Multi-Unit Mapping
	// Create TV show & units (S01E01, S01E02)
	tvID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: tvID, Title: "Breaking Bad", MediaType: "tv"})
	s1e1ID, _ := uuid.NewV7()
	_, _ = queries.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: s1e1ID, MediaID: tvID, UnitKey: "S01E01", SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 1, Valid: true}})
	s1e2ID, _ := uuid.NewV7()
	_, _ = queries.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: s1e2ID, MediaID: tvID, UnitKey: "S01E02", SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 2, Valid: true}})

	// Parser parses multi-episode filename
	multiRec := domainMedia.RecognizeRelease("Breaking.Bad.S01E01-E02.1080p.mkv")
	if multiRec.Season != 1 || len(multiRec.Episodes) != 2 || multiRec.Episodes[0] != 1 || multiRec.Episodes[1] != 2 {
		t.Fatalf("multi-episode release parsing failed: %+v", multiRec)
	}

	// Create physical multi-episode file on disk
	multiRelPath := "Breaking Bad/Season 01/Breaking Bad S01E01-E02 [1080p].mkv"
	multiFullPath := filepath.Join(tvDir, multiRelPath)
	_ = os.MkdirAll(filepath.Dir(multiFullPath), 0755)
	multiPayload := []byte("MULTI_EPISODE_VIDEO_DATA_12345")
	_ = os.WriteFile(multiFullPath, multiPayload, 0644)

	multiFileID, _ := uuid.NewV7()
	multiFile, err := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              multiFileID,
		RootID:          tvRootID,
		RelPath:         multiRelPath,
		SizeBytes:       int64(len(multiPayload)),
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	if err != nil {
		t.Fatalf("failed to insert multi-ep file: %v", err)
	}

	// Link single file to BOTH units
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: multiFile.ID, UnitID: s1e1ID})
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: multiFile.ID, UnitID: s1e2ID})

	// Verify both units map to the same file
	f1, _ := queries.ListFilesForUnit(ctx, s1e1ID)
	f2, _ := queries.ListFilesForUnit(ctx, s1e2ID)
	if len(f1) != 1 || len(f2) != 1 || f1[0].ID != multiFile.ID || f2[0].ID != multiFile.ID {
		t.Fatalf("multi-unit mapping verification failed: f1=%+v, f2=%+v", f1, f2)
	}

	// 2. Scenario: Quality Upgrade & Historical Confirmation Invalidation
	// Create Movie and initial 1080p file
	movID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: movID, Title: "The Dark Knight", MediaType: "movie"})
	movUnitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: movUnitID, MediaID: movID, UnitKey: "movie"})

	file1080Rel := "The Dark Knight (2008)/The Dark Knight [1080p].mkv"
	file1080Full := filepath.Join(moviesDir, file1080Rel)
	_ = os.MkdirAll(filepath.Dir(file1080Full), 0755)
	payload1080 := []byte("DATA_1080P_8GB")
	_ = os.WriteFile(file1080Full, payload1080, 0644)

	file1080ID, _ := uuid.NewV7()
	file1080, err := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              file1080ID,
		RootID:          movRootID,
		RelPath:         file1080Rel,
		SizeBytes:       int64(len(payload1080)),
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	if err != nil {
		t.Fatalf("insert 1080p file failed: %v", err)
	}
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file1080.ID, UnitID: movUnitID})

	// Initial confirmed fact on 1080p file
	fact1080ID, _ := uuid.NewV7()
	_, _ = queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             fact1080ID,
		UnitID:         movUnitID,
		Stage:          "confirmed",
		SourceFileID:   pgtype.UUID{Bytes: file1080.ID, Valid: true},
		RootID:         pgtype.UUID{Bytes: movRootID, Valid: true},
		QualityProfile: []byte(`{"resolution":"1080p","source":"WEB-DL"}`),
	})

	// Explicit source capability for this isolated fixture, not a production /tmp allow-list.
	if _, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: tempDir, MediaType: "movie", Name: "Fixture source"}); err != nil {
		t.Fatalf("configure source root: %v", err)
	}

	// Download new 4K file
	file4KDownloaded := filepath.Join(tempDir, "The.Dark.Knight.2008.2160p.Remux.mkv")
	payload4K := []byte("DATA_4K_REMUX_25GB_123456789")
	_ = os.WriteFile(file4KDownloaded, payload4K, 0644)

	// Perform quality upgrade via UpgradeMediaFile
	ob := outbox.New(pool, gen, mockClock)
	transferSvc := transfer.NewService(transferstore.NewRepository(pool, gen, ob, rootfs.NewFilesystem(), transfer.ExecutePreparedPlan))
	q4K := quality.Profile{Resolution: quality.Res2160p, Source: quality.SourceRemux}

	upgradeOpID, _ := uuid.NewV7()
	err = transferSvc.UpgradeMediaFile(ctx, upgradeOpID, movRootID, movUnitID, file1080.ID, file4KDownloaded, "The Dark Knight", 2008, q4K)
	if err != nil {
		t.Fatalf("upgrade media file failed: %v", err)
	}

	// Verify old 1080p file is retired
	oldFile, err := queries.GetFileByID(ctx, file1080.ID)
	if err != nil || !oldFile.RetiredAt.Valid {
		t.Fatalf("expected old 1080p file to be retired: %+v", oldFile)
	}

	// Invariant: "旧确认不制造拥有" -> Old fact for 1080p file MUST be revoked!
	var revokedCount int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE source_file_id = $1 AND revoked_at IS NOT NULL", file1080.ID).Scan(&revokedCount)
	if revokedCount != 1 {
		t.Fatalf("historical confirmation fact must be revoked when file retired: got count=%d", revokedCount)
	}

	// Active fact must be the new imported fact
	activeFacts, err := queries.ListActiveFactsForUnit(ctx, movUnitID)
	if err != nil || len(activeFacts) != 1 || activeFacts[0].Stage != "imported" {
		t.Fatalf("expected 1 active imported fact for upgraded movie: %+v", activeFacts)
	}

	// 3. Scenario: Cross-Root Reuse
	// Movie "Gladiator" is present in Archive Root B, needed in Movies Root Primary
	gladID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: gladID, Title: "Gladiator", MediaType: "movie"})
	gladUnitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: gladUnitID, MediaID: gladID, UnitKey: "movie"})

	gladArchiveRel := "Gladiator (2000)/Gladiator (2000) [Remux 2160p].mkv"
	gladArchiveFull := filepath.Join(archiveDir, gladArchiveRel)
	_ = os.MkdirAll(filepath.Dir(gladArchiveFull), 0755)
	gladPayload := []byte("GLADIATOR_4K_REMUX_PAYLOAD_12345")
	_ = os.WriteFile(gladArchiveFull, gladPayload, 0644)

	gladArchiveFileID, _ := uuid.NewV7()
	gladArchiveFile, _ := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              gladArchiveFileID,
		RootID:          arcRootID,
		RelPath:         gladArchiveRel,
		SizeBytes:       int64(len(gladPayload)),
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: gladArchiveFile.ID, UnitID: gladUnitID})

	slotLocker := legacytransfer.NewSlotLocker()
	reuseSvc := libraryreuse.NewReuseService(pool, gen, slotLocker)

	// Reuse discovers file in Archive and copies to Movies Primary with full SHA-256 validation
	reuseCand, err := reuseSvc.FindCrossRootCandidate(ctx, movRootID, gladUnitID, quality.DefaultPolicy())
	if err != nil || reuseCand.File.ID != gladArchiveFile.ID {
		t.Fatalf("find reuse candidate failed: %v", err)
	}

	reuseOpID, _ := uuid.NewV7()
	err = reuseSvc.ExecuteCrossRootReuse(ctx, reuseOpID, arcRootID, movRootID, gladUnitID, gladArchiveFile.ID, "Gladiator", 2000, q4K, "copy")
	if err != nil {
		t.Fatalf("cross-root reuse execution failed: %v", err)
	}

	// Verify Movies Primary now has Gladiator file
	gladPrimaryFile, err := queries.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{
		RootID:  movRootID,
		RelPath: "Gladiator (2000)/Gladiator (2000) [Remux 2160p].mkv",
	})
	if err != nil || gladPrimaryFile.SizeBytes != int64(len(gladPayload)) {
		t.Fatalf("reused file not properly registered in Primary Root: %v", err)
	}

	_ = adminActor
	_ = usecaseSearch.ErrRoundAborted
	_ = subscription.ErrInvalidState
	_ = policy.ErrPolicyNotFound
	_ = acquisition.ErrAlreadyOwned
}
