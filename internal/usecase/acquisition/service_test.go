package acquisition_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
)

const migrationsDir = "../../../migrations"

func setupAcquisitionTest(t *testing.T) (*acquisition.Service, *db.Queries, uuid.UUID, uuid.UUID, uuid.UUID) {
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
	svc := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, c, acquisition.SelectMovieRound))
	svc.SetRequirementRepository(postgres.NewDownloadSubmissionStore(pool, gen))
	queries := postgres.Queries(pool)

	// Create root
	rootID, _ := uuid.NewV7()
	_, err = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/media/movies",
		MediaType: "movie",
		Name:      "Movies",
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}

	if _, err = pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", rootID); err != nil {
		t.Fatal(err)
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

	// Create indexer & candidate
	indexerID, _ := uuid.NewV7()
	_, _ = queries.CreateIndexer(ctx, db.CreateIndexerParams{
		ID:              indexerID,
		Name:            "Tracker",
		Url:             "https://tracker.com",
		ApiKeyEncrypted: []byte("enc"),
		Categories:      []byte("[]"),
	})

	candID, _ := uuid.NewV7()
	_, err = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{SourceConfigVersion: 1,
		ID:             candID,
		IndexerID:      indexerID,
		Title:          "Fight.Club.1999.1080p.mkv",
		Guid:           "guid-1",
		LinkEncrypted:  []byte("enc_link"),
		SizeBytes:      8000000000,
		QualityProfile: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("failed to create release candidate: %v", err)
	}

	return svc, queries, rootID, unitID, candID
}

func TestAcquisitionAlreadyOwnedAndReservationCollision(t *testing.T) {
	svc, queries, rootID, unitID, candID := setupAcquisitionTest(t)
	ctx := context.Background()

	adminActor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   uuid.New().String(),
		Name: "admin",
		Role: ports.RoleAdmin,
	}

	pgtest.CreateUserActor(t, queries, adminActor)
	// 1. Initial acquisition: must succeed and create reservation, requirement, job, outbox event
	cmd := acquisition.AcquireMovieCommand{
		UnitIDs:     []uuid.UUID{unitID},
		RootID:      rootID,
		CandidateID: candID,
	}

	res, err := svc.AcquireMovie(ctx, adminActor, cmd)
	if err != nil {
		t.Fatalf("acquisition failed: %v", err)
	}
	if len(res.Reservations) != 1 || res.Status != "dispatched" {
		t.Fatalf("unexpected acquire result: %+v", res)
	}

	// 2. Concurrent/duplicate acquisition for the SAME unit in the SAME root: must fail with ErrUnitAlreadyReserved!
	_, err = svc.AcquireMovie(ctx, adminActor, cmd)
	if !errors.Is(err, acquisition.ErrUnitAlreadyReserved) {
		t.Fatalf("expected ErrUnitAlreadyReserved on duplicate acquisition, got: %v", err)
	}

	// 3. Mark unit as owned (insert library file and link unit)
	fileID, _ := uuid.NewV7()
	file, _ := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:        fileID,
		RootID:    rootID,
		RelPath:   "Fight Club (1999)/Fight Club.mkv",
		SizeBytes: 8000000000,
		MtimeNs:   time.Now().UnixNano(),
	})
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{
		FileID: file.ID,
		UnitID: unitID,
	})

	if _, err := queries.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unitID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, RootID: pgtype.UUID{Bytes: rootID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`)}); err != nil {
		t.Fatal(err)
	}
	// Release previous reservation to isolate ownership check
	_, _ = queries.UpdateReservationState(ctx, db.UpdateReservationStateParams{
		ID:    res.Reservations[0],
		State: "released",
	})

	// 4. Invariant: Do not acquire already owned movies (不抓已拥有电影!)
	_, err = svc.AcquireMovie(ctx, adminActor, cmd)
	if !errors.Is(err, acquisition.ErrAlreadyOwned) {
		t.Fatalf("expected ErrAlreadyOwned for already owned movie, got: %v", err)
	}
}

func TestUnsupportedAcquisitionCoverageHasNoPartialReservations(t *testing.T) {
	svc, queries, rootID, unit1ID, candID := setupAcquisitionTest(t)
	ctx := context.Background()

	// Create a second unit (e.g. Episode 2)
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Show", MediaType: "tv"})
	unit2ID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit2ID, MediaID: mediaID, UnitKey: "S01E02"})

	adminActor := pgtest.CreateUserActor(t, queries, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.New().String(), Name: "admin", Role: ports.RoleAdmin})

	// Pre-reserve unit2 to force collision on unit2
	preResID, _ := uuid.NewV7()
	_, _ = queries.CreateUnitReservation(ctx, db.CreateUnitReservationParams{
		ID:          preResID,
		UnitID:      unit2ID,
		RootID:      rootID,
		Purpose:     "manual_acquisition",
		CandidateID: candID,
		OperationID: uuid.New(),
	})

	// Attempt to acquire both unit1 and unit2 together (e.g. season pack)
	packCmd := acquisition.AcquireMovieCommand{
		UnitIDs:     []uuid.UUID{unit1ID, unit2ID},
		RootID:      rootID,
		CandidateID: candID,
	}

	_, err := svc.AcquireMovie(ctx, adminActor, packCmd)
	if !errors.Is(err, acquisition.ErrUnsupportedCoverage) {
		t.Fatalf("expected unsupported coverage, got: %v", err)
	}

	// All-or-nothing verification: Unit 1 must NOT be reserved!
	res1, err := queries.GetActiveReservation(ctx, db.GetActiveReservationParams{
		UnitID:  unit1ID,
		RootID:  rootID,
		Purpose: "manual_acquisition",
	})
	if err == nil {
		t.Fatalf("all-or-nothing invariant violated: unit 1 was reserved despite pack failure: %+v", res1)
	}
}
func TestSharedAcquisitionAndResidualRequirements(t *testing.T) {
	svc, queries, rootID, unitID, candID := setupAcquisitionTest(t)
	ctx := context.Background()

	actorA := pgtest.CreateUserActor(t, queries, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.New().String(), Name: "userA", Role: ports.RoleMember})
	actorB := pgtest.CreateUserActor(t, queries, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.New().String(), Name: "userB", Role: ports.RoleMember})

	// 1. Actor A acquires unit
	resA, err := svc.AcquireMovie(ctx, actorA, acquisition.AcquireMovieCommand{
		UnitIDs:     []uuid.UUID{unitID},
		RootID:      rootID,
		CandidateID: candID,
	})
	if err != nil {
		t.Fatalf("actor A acquire failed: %v", err)
	}

	// 2. Actor B attaches requirement to the same operation (sharing the physical download)
	err = svc.AttachRequirement(ctx, actorB, resA.OperationID, unitID, rootID)
	if err != nil {
		t.Fatalf("actor B attach requirement failed: %v", err)
	}

	reqs, err := queries.ListRequirementsForOperation(ctx, resA.OperationID)
	if err != nil || len(reqs) != 2 {
		t.Fatalf("expected 2 requirements for operation: %+v", reqs)
	}

	// 3. Actor A cancels / releases their requirement
	released, err := svc.ReleaseRequirement(ctx, actorA, resA.OperationID, unitID, rootID)
	if err != nil {
		t.Fatalf("release A failed: %v", err)
	}
	if released {
		t.Fatal("reservation should NOT be released while Actor B still requires it!")
	}

	// Verify reservation is still active
	activeRes, err := queries.GetActiveReservation(ctx, db.GetActiveReservationParams{
		UnitID:  unitID,
		RootID:  rootID,
		Purpose: "manual_acquisition",
	})
	if err != nil || activeRes.State != "reserved" {
		t.Fatalf("reservation must remain active for Actor B: %+v", activeRes)
	}

	// 4. Actor B also releases requirement: now no requirements remain -> reservation released!
	released2, err := svc.ReleaseRequirement(ctx, actorB, resA.OperationID, unitID, rootID)
	if err != nil {
		t.Fatalf("release B failed: %v", err)
	}
	if !released2 {
		t.Fatal("reservation should now be released as 0 requirements remain")
	}

	_, err = queries.GetActiveReservation(ctx, db.GetActiveReservationParams{
		UnitID:  unitID,
		RootID:  rootID,
		Purpose: "manual_acquisition",
	})
	if err == nil {
		t.Fatal("active reservation should no longer exist after all requirements released")
	}
}
