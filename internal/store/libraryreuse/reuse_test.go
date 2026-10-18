package library_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	transfer "github.com/dalamudx/PulseMedia/internal/adapter/transferlegacy"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	library "github.com/dalamudx/PulseMedia/internal/store/libraryreuse"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func setupReuseTest(t *testing.T) (*library.ReuseService, *db.Queries, uuid.UUID, uuid.UUID, uuid.UUID, string, string) {
	t.Helper()
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, "../../../migrations"); err != nil {
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
	slotLocker := transfer.NewSlotLocker()
	reuseSvc := library.NewReuseService(pool, gen, slotLocker)
	queries := postgres.Queries(pool)

	tempDir, err := os.MkdirTemp("", "pulsemedia-reuse-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	rootAPath := filepath.Join(tempDir, "movies_primary")
	rootBPath := filepath.Join(tempDir, "movies_archive")
	_ = os.MkdirAll(rootAPath, 0755)
	_ = os.MkdirAll(rootBPath, 0755)

	// Create Root A (target)
	rootAID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootAID,
		Path:      rootAPath,
		MediaType: "movie",
		Name:      "Root Primary",
	})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootAID)

	// Create Root B (source archive)
	rootBID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootBID,
		Path:      rootBPath,
		MediaType: "movie",
		Name:      "Root Archive",
	})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootBID)

	// Create media & unit
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Interstellar", MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})

	return reuseSvc, queries, rootAID, rootBID, unitID, rootAPath, rootBPath
}

func TestCrossRootReuseDiscoveryAndExecution(t *testing.T) {
	reuseSvc, queries, rootAID, rootBID, unitID, _, rootBPath := setupReuseTest(t)
	ctx := context.Background()

	// 1. Initial state: neither root has the movie file -> FindCrossRootCandidate returns ErrNoReuseCandidate
	policy := quality.DefaultPolicy()
	_, err := reuseSvc.FindCrossRootCandidate(ctx, rootAID, unitID, policy)
	if err == nil {
		t.Fatal("expected ErrNoReuseCandidate when no file exists in other roots")
	}

	// 2. Archive Root B receives the file (e.g. from historical library or archive import)
	sourceRelPath := "Interstellar (2014)/Interstellar (2014) [Remux 2160p].mkv"
	sourceFullPath := filepath.Join(rootBPath, sourceRelPath)
	_ = os.MkdirAll(filepath.Dir(sourceFullPath), 0755)
	payload := []byte("REAL_4K_REUSE_PAYLOAD_123456789")
	_ = os.WriteFile(sourceFullPath, payload, 0644)

	fileID, _ := uuid.NewV7()
	fileB, err := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              fileID,
		RootID:          rootBID,
		RelPath:         sourceRelPath,
		SizeBytes:       int64(len(payload)),
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	if err != nil {
		t.Fatalf("failed to insert archive file: %v", err)
	}
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: fileB.ID, UnitID: unitID})

	factID, _ := uuid.NewV7()
	_, _ = queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             factID,
		UnitID:         unitID,
		Stage:          "imported",
		SourceFileID:   pgtype.UUID{Bytes: fileB.ID, Valid: true},
		RootID:         pgtype.UUID{Bytes: rootBID, Valid: true},
		QualityProfile: []byte(`{"resolution":"2160p","resolution_parser_version":"resolution.tokens.v1","source":"Remux","source_parser_version":"source.tokens.v2"}`),
	})

	// 3. Now Root A searches for reuse candidates: finds the file in Root B! ("优先复用而非新抓")
	cand, err := reuseSvc.FindCrossRootCandidate(ctx, rootAID, unitID, policy)
	if err != nil {
		t.Fatalf("find cross root candidate failed: %v", err)
	}
	if cand.File.ID != fileB.ID || cand.SourceRoot.ID != rootBID || cand.Score <= 0 {
		t.Fatalf("unexpected candidate: %+v", cand)
	}

	// 4. Execute Cross-Root Reuse from Root B to Root A using copy strategy with full SHA-256 validation
	opID, _ := uuid.NewV7()
	q := quality.Profile{Resolution: quality.Res2160p, Source: quality.SourceRemux}

	err = reuseSvc.ExecuteCrossRootReuse(ctx, opID, rootBID, rootAID, unitID, fileB.ID, "Interstellar", 2014, q, "copy")
	if err != nil {
		t.Fatalf("execute cross root reuse failed: %v", err)
	}

	// 5. Verify Target Root A now has an active library file and imported fact!
	filesInRootA, err := queries.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{
		RootID:  rootAID,
		RelPath: "Interstellar (2014)/Interstellar (2014) [Remux 2160p].mkv",
	})
	if err != nil || filesInRootA.SizeBytes != int64(len(payload)) {
		t.Fatalf("reused file not properly registered in Root A: %v", err)
	}
}
