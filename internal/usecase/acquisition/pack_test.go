package acquisition_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

func setupSeasonPackTest(t *testing.T) (*acquisition.Service, *db.Queries, uuid.UUID, []uuid.UUID, uuid.UUID, uuid.UUID) {
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
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	acqSvc := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, mockClock, acquisition.SelectMovieRound))
	queries := postgres.Queries(pool)

	// Create root
	rootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/data/tv",
		MediaType: "tv",
		Name:      "TV Shows",
	})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootID)

	// Create TV show
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Breaking Bad", MediaType: "tv"})

	// Create 5 episodes (S01E01 to S01E05)
	unitIDs := make([]uuid.UUID, 5)
	for i := 1; i <= 5; i++ {
		uID, _ := uuid.NewV7()
		unitIDs[i-1] = uID
		_, _ = queries.CreateTVUnit(ctx, db.CreateTVUnitParams{
			ID:            uID,
			MediaID:       mediaID,
			UnitKey:       media.FormatEpisodeUnitKey(1, i),
			SeasonNumber:  pgtype.Int4{Int32: 1, Valid: true},
			EpisodeNumber: pgtype.Int4{Int32: int32(i), Valid: true},
		})
	}

	// Create indexer & pack candidate
	indexerID, _ := uuid.NewV7()
	_, _ = queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID:              indexerID,
		Name:            "Tracker",
		Url:             "https://tracker.com",
		ApiKeyEncrypted: []byte("enc"),
		Categories:      []byte("[]"),
	})

	packCandID, _ := uuid.NewV7()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             packCandID,
		IndexerID:      indexerID,
		Title:          "Breaking.Bad.S01.Complete.1080p",
		Guid:           "guid-pack",
		LinkEncrypted:  []byte("link"),
		SizeBytes:      20000000000,
		QualityProfile: []byte("{}"),
	})

	return acqSvc, queries, rootID, unitIDs, packCandID, indexerID
}

func TestUnsupportedPackPreservesExistingSingleReservation(t *testing.T) {
	acqSvc, queries, rootID, unitIDs, packCandID, indexerID := setupSeasonPackTest(t)
	ctx := context.Background()

	adminActor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   uuid.New().String(),
		Name: "admin",
		Role: ports.RoleAdmin,
	}

	pgtest.CreateUserActor(t, queries, adminActor)
	// 1. Single episode E03 is already active reserved by a single-episode download task!
	singleCandID, _ := uuid.NewV7()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             singleCandID,
		IndexerID:      indexerID,
		Title:          "Breaking.Bad.S01E03.1080p",
		Guid:           "guid-e03",
		LinkEncrypted:  []byte("link"),
		SizeBytes:      4000000000,
		QualityProfile: []byte("{}"),
	})

	e03ResID, _ := uuid.NewV7()
	_, err := queries.CreateUnitReservation(ctx, db.CreateUnitReservationParams{
		ID:          e03ResID,
		UnitID:      unitIDs[2], // Episode 3
		RootID:      rootID,
		Purpose:     "manual_acquisition",
		CandidateID: singleCandID,
		OperationID: uuid.New(),
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("pre-reservation of E03 failed: %v", err)
	}
	protected, err := queries.GetActiveReservation(ctx, db.GetActiveReservationParams{
		UnitID: unitIDs[2], RootID: rootID, Purpose: "manual_acquisition",
	})
	if err != nil {
		t.Fatalf("read protected E03 reservation before pack refusal: %v", err)
	}

	// 2. Season pack acquisition attempts to acquire the entire season (E01-E05):
	// Until complete frozen coverage is implemented, this generic movie UoW must refuse the pack.
	packCmd := acquisition.AcquireMovieCommand{
		UnitIDs:     unitIDs, // all 5 episodes
		RootID:      rootID,
		CandidateID: packCandID,
	}

	_, err = acqSvc.AcquireMovie(ctx, adminActor, packCmd)
	if !errors.Is(err, acquisition.ErrUnsupportedCoverage) {
		t.Fatalf("expected unsupported coverage, got: %v", err)
	}

	// 3. Invariant: "包/单集竞争不部分占位"！
	// Unit 1, Unit 2, Unit 4, Unit 5 must NOT be partially reserved!
	for i, uID := range unitIDs {
		if i == 2 {
			continue // skip E03 which was pre-reserved
		}
		res, err := queries.GetActiveReservation(ctx, db.GetActiveReservationParams{
			UnitID:  uID,
			RootID:  rootID,
			Purpose: "manual_acquisition",
		})
		if err == nil {
			t.Fatalf("partial reservation invariant violated: unit E%02d was partially reserved despite pack rollback: %+v", i+1, res)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("cannot verify absence of E%02d reservation: %v", i+1, err)
		}
	}
	retained, err := queries.GetActiveReservation(ctx, db.GetActiveReservationParams{
		UnitID: unitIDs[2], RootID: rootID, Purpose: "manual_acquisition",
	})
	if err != nil {
		t.Fatalf("pack refusal removed protected E03 reservation: %v", err)
	}
	if retained.ID != protected.ID || retained.CandidateID != protected.CandidateID || retained.OperationID != protected.OperationID || retained.ExpiresAt.Valid != protected.ExpiresAt.Valid || !retained.ExpiresAt.Time.Equal(protected.ExpiresAt.Time) {
		t.Fatal("pack refusal replaced or changed the protected single-episode reservation")
	}
}

func TestRequirePackPolicyRejectsIncompleteSeasonPacks(t *testing.T) {
	reqPolicy := quality.PackPolicy{
		Mode: quality.PackModeRequirePack,
	}

	incompletePack := quality.PackCandidate{
		ID:             "pack-incomplete",
		Title:          "Breaking.Bad.S01.Part1",
		Season:         1,
		StartEp:        1,
		EndEp:          3,
		TotalSeasonEps: 5,
	}

	err := quality.EvaluatePackCompleteness(reqPolicy, incompletePack)
	if !errors.Is(err, quality.ErrIncompletePack) {
		t.Fatalf("expected ErrIncompletePack for incomplete pack under require_pack, got: %v", err)
	}
}
