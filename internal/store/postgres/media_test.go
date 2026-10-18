package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)



func TestMediaIdentityUniqueness(t *testing.T) {
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

	queries := postgres.Queries(pool)

	m1ID, _ := uuid.NewV7()
	m1, err := queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        m1ID,
		Title:     "Fight Club",
		MediaType: "movie",
	})
	if err != nil {
		t.Fatalf("failed to create media 1: %v", err)
	}

	// 1. Insert identity
	identID, _ := uuid.NewV7()
	_, err = queries.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{
		ID:         identID,
		MediaID:    m1.ID,
		Namespace:  "tmdb.movie",
		ExternalID: "550",
	})
	if err != nil {
		t.Fatalf("failed to insert identity: %v", err)
	}

	// 2. Duplicate identity for another media must fail with 23505 unique violation
	m2ID, _ := uuid.NewV7()
	m2, _ := queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        m2ID,
		Title:     "Another Movie",
		MediaType: "movie",
	})

	ident2ID, _ := uuid.NewV7()
	_, err = queries.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{
		ID:         ident2ID,
		MediaID:    m2.ID,
		Namespace:  "tmdb.movie",
		ExternalID: "550",
	})
	if err == nil {
		t.Fatal("expected duplicate identity to fail with unique violation")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("expected 23505 unique violation, got: %v", err)
	}
}

func TestLibraryFileVersioningOnSamePath(t *testing.T) {
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

	queries := postgres.Queries(pool)

	rootID, _ := uuid.NewV7()
	root, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/media/movies",
		MediaType: "movie",
		Name:      "Movies",
	})
	if err != nil {
		t.Fatalf("failed to create library root: %v", err)
	}

	relPath := "Fight Club (1999)/Fight Club (1999) [1080p].mkv"

	// 1. Initial file v1 at path
	f1ID, _ := uuid.NewV7()
	f1, err := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:               f1ID,
		RootID:           root.ID,
		RelPath:          relPath,
		SizeBytes:        8000000000,
		MtimeNs:          time.Now().UnixNano(),
		LastMutationSeq:  1,
	})
	if err != nil {
		t.Fatalf("failed to create file v1: %v", err)
	}

	// Concurrent second file at SAME path without retiring old fails
	fConflictID, _ := uuid.NewV7()
	_, err = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:               fConflictID,
		RootID:           root.ID,
		RelPath:          relPath,
		SizeBytes:        9000000000,
		MtimeNs:          time.Now().UnixNano(),
		LastMutationSeq:  2,
	})
	if err == nil {
		t.Fatal("expected duplicate active file at same path to fail")
	}

	// 2. Retire file v1
	err = queries.RetireFileByPath(ctx, db.RetireFileByPathParams{
		RootID:  root.ID,
		RelPath: relPath,
	})
	if err != nil {
		t.Fatalf("failed to retire file v1: %v", err)
	}

	// 3. New file v2 (e.g. 4K upgrade) can now take the same path!
	f2ID, _ := uuid.NewV7()
	f2, err := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:               f2ID,
		RootID:           root.ID,
		RelPath:          relPath,
		SizeBytes:        25000000000,
		MtimeNs:          time.Now().UnixNano(),
		LastMutationSeq:  3,
	})
	if err != nil {
		t.Fatalf("failed to create file v2 at same path after v1 retired: %v", err)
	}
	if f2.ID == f1.ID {
		t.Fatal("file identity must change on upgrade")
	}

	// Active file lookup returns f2
	active, err := queries.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{
		RootID:  root.ID,
		RelPath: relPath,
	})
	if err != nil || active.ID != f2.ID {
		t.Fatalf("expected active file to be f2 (%s), got: %v", f2.ID, active.ID)
	}
}

func TestMultiUnitMappingAndFactConstraints(t *testing.T) {
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

	queries := postgres.Queries(pool)

	// Create TV show with 2 episodes: S01E01 and S01E02
	tvID, _ := uuid.NewV7()
	tv, _ := queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        tvID,
		Title:     "Breaking Bad",
		MediaType: "tv",
	})

	u1ID, _ := uuid.NewV7()
	u1, _ := queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{
		ID:       u1ID,
		MediaID:  tv.ID,
		UnitKey:  "S01E01",
	})

	u2ID, _ := uuid.NewV7()
	u2, _ := queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{
		ID:       u2ID,
		MediaID:  tv.ID,
		UnitKey:  "S01E02",
	})

	rootID, _ := uuid.NewV7()
	root, _ := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/media/tv",
		MediaType: "tv",
		Name:      "TV Shows",
	})

	// One file covering both E01 and E02 (e.g. S01E01-E02.mkv)
	fileID, _ := uuid.NewV7()
	file, _ := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:        fileID,
		RootID:    root.ID,
		RelPath:   "Breaking Bad/Season 01/Breaking Bad S01E01-E02.mkv",
		SizeBytes: 4000000000,
		MtimeNs:   time.Now().UnixNano(),
	})

	// Link file to BOTH units
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: u1.ID})
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: u2.ID})

	units, err := queries.ListUnitsForFile(ctx, file.ID)
	if err != nil || len(units) != 2 {
		t.Fatalf("expected 2 units for combined file, got %d (err: %v)", len(units), err)
	}

	// 1. Fact constraint: stage='imported' WITHOUT source_file_id must fail CHECK constraint
	factBadID, _ := uuid.NewV7()
	_, err = queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             factBadID,
		UnitID:         u1.ID,
		Stage:          "imported",
		SourceFileID:   pgtype.UUID{Valid: false},
		QualityProfile: []byte(`{"resolution":"1080p"}`),
	})
	if err == nil {
		t.Fatal("expected CHECK constraint violation for imported fact without file")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" { // 23514 = check_violation
		t.Fatalf("expected 23514 check violation, got: %v", err)
	}

	// 2. Fact constraint: stage='imported' WITH source_file_id succeeds
	factGoodID, _ := uuid.NewV7()
	fact, err := queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             factGoodID,
		UnitID:         u1.ID,
		Stage:          "imported",
		SourceFileID:   pgtype.UUID{Bytes: file.ID, Valid: true},
		QualityProfile: []byte(`{"resolution":"1080p","source":"WEB-DL"}`),
	})
	if err != nil {
		t.Fatalf("expected valid imported fact to succeed: %v", err)
	}
	if fact.Stage != "imported" {
		t.Fatalf("unexpected stage: %s", fact.Stage)
	}
}
