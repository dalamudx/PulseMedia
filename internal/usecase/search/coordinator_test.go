package search_test

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
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func setupCoordinatorTest(t *testing.T) (*usecaseSearch.RoundCoordinator, *subscription.Service, *policy.Service, *db.Queries, *ports.Actor, uuid.UUID, uuid.UUID, uuid.UUID) {
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
	polSvc := policy.NewService(postgres.NewPolicyStore(pool, gen))
	subSvc := newTestSubscriptionService(pool, gen)
	coord := usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStore(pool, gen), mockClock, acqSvc)
	queries := postgres.Queries(pool)

	adminID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{ID: adminID, Username: "admin", PasswordHash: "h", Role: ports.RoleAdmin, IsActive: true})
	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: adminID.String(), Name: "admin", Role: ports.RoleAdmin}

	// Create root with baseline completed
	rootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: rootID, Path: "/data/movies", MediaType: "movie", Name: "Movies"})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootID)

	// Create media & unit
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Fight Club", Year: pgtype.Int4{Int32: 1999, Valid: true}, MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})

	// Create indexer
	indexerID, _ := uuid.NewV7()
	_, _ = queries.CreateIndexer(ctx, db.CreateIndexerParams{ID: indexerID, Name: "Trk", Url: "https://trk.org", ApiKeyEncrypted: []byte("k"), Categories: []byte("[]")})

	// Create candidate
	candID, _ := uuid.NewV7()
	_, _ = queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{
		ID:             candID,
		IndexerID:      indexerID,
		Title:          "Fight.Club.1999.1080p.BluRay.x264-SPARKS",
		Guid:           "guid-1080p",
		LinkEncrypted:  []byte("enc"),
		SizeBytes:      8000000000,
		QualityProfile: []byte(`{"resolution":"1080p","source":"Bluray"}`), SourceConfigVersion: 1,
	})

	return coord, subSvc, polSvc, queries, adminActor, rootID, mediaID, candID
}

func TestSearchRoundSnapshotAndStaleRoundRejection(t *testing.T) {
	coord, subSvc, polSvc, queries, adminActor, rootID, mediaID, candID := setupCoordinatorTest(t)
	ctx := context.Background()

	// Create subscription
	sub, wantedItems, err := subSvc.CreateSubscription(ctx, adminActor, subscription.CreateSubscriptionCommand{
		MediaID:   mediaID,
		RootID:    rootID,
		ScopeType: "all",
	})
	if err != nil || len(wantedItems) != 1 {
		t.Fatalf("create sub failed: %v", err)
	}
	wantedID := wantedItems[0].ID

	// 1. Start SearchRound
	round, err := coord.StartRound(ctx, wantedID, 30*time.Second)
	if err != nil {
		t.Fatalf("start round failed: %v", err)
	}
	if round.State != "running" || round.SnapshotIntentVersion != 1 || round.SnapshotPolicyRevision != 1 {
		t.Fatalf("unexpected round snapshot: %+v", round)
	}

	// 2. Add qualified candidate to round
	_ = coord.AddCandidate(ctx, round.ID, candID)

	// 3. User PAUSES subscription while round is in flight!
	_, err = subSvc.UpdateState(ctx, adminActor, sub.ID, "paused")
	if err != nil {
		t.Fatalf("pause sub failed: %v", err)
	}

	// 4. Invariant: Finalizing a round when subscription is paused MUST abort the round and reject acquisition!
	repAborted, err := coord.FinalizeRound(ctx, adminActor, round.ID)
	if !errors.Is(err, usecaseSearch.ErrRoundAborted) {
		t.Fatalf("expected ErrRoundAborted on paused sub, got: %v", err)
	}
	if repAborted.State != "aborted" || repAborted.Reason != "subscription_paused" {
		t.Fatalf("unexpected abort report: %+v", repAborted)
	}

	// 5. Resume subscription and start round 2
	_, _ = subSvc.UpdateState(ctx, adminActor, sub.ID, "active") // bumps intent_version to 3

	round2, err := coord.StartRound(ctx, wantedID, 30*time.Second)
	if err != nil {
		t.Fatalf("start round 2 failed: %v", err)
	}
	_ = coord.AddCandidate(ctx, round2.ID, candID)

	// Finalize round 2: succeeds and submits acquisition!
	repSuccess, err := coord.FinalizeRound(ctx, adminActor, round2.ID)
	if err != nil {
		t.Fatalf("finalize round 2 failed: %v", err)
	}
	if repSuccess.State != "completed" || repSuccess.SelectedCandidateID == nil || *repSuccess.SelectedCandidateID != candID {
		t.Fatalf("expected completed round with selected candidate: %+v", repSuccess)
	}

	_ = polSvc
	_ = queries
}
