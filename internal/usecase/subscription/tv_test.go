package subscription_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func setupTVTest(t *testing.T) (*subscription.Service, *subscription.Evaluator, *db.Queries, *ports.Actor, uuid.UUID, uuid.UUID) {
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
	subSvc := newIntentService(pool, gen)
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	queries := postgres.Queries(pool)

	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.New().String(), Name: "admin", Role: ports.RoleAdmin}
	adminUUID, _ := uuid.Parse(adminActor.ID)
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{ID: adminUUID, Username: "admin", PasswordHash: "h", Role: ports.RoleAdmin, IsActive: true})

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

	// Create S01E01 (aired past)
	s1e1ID, _ := uuid.NewV7()
	_, _ = queries.CreateTVUnit(ctx, db.CreateTVUnitParams{
		ID:             s1e1ID,
		MediaID:        mediaID,
		UnitKey:        "S01E01",
		SeasonNumber:   pgtype.Int4{Int32: 1, Valid: true},
		EpisodeNumber:  pgtype.Int4{Int32: 1, Valid: true},
		AbsoluteNumber: pgtype.Int4{Int32: 1, Valid: true},
		AirDate:        pgtype.Date{Time: time.Date(2008, 1, 20, 0, 0, 0, 0, time.UTC), Valid: true},
	})

	// Create S01E02 (future un-aired: year 2027)
	s1e2ID, _ := uuid.NewV7()
	_, _ = queries.CreateTVUnit(ctx, db.CreateTVUnitParams{
		ID:             s1e2ID,
		MediaID:        mediaID,
		UnitKey:        "S01E02",
		SeasonNumber:   pgtype.Int4{Int32: 1, Valid: true},
		EpisodeNumber:  pgtype.Int4{Int32: 2, Valid: true},
		AbsoluteNumber: pgtype.Int4{Int32: 2, Valid: true},
		AirDate:        pgtype.Date{Time: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	})

	// Create S00E01 (Special: S00)
	s0e1ID, _ := uuid.NewV7()
	_, _ = queries.CreateTVUnit(ctx, db.CreateTVUnitParams{
		ID:             s0e1ID,
		MediaID:        mediaID,
		UnitKey:        "S00E01",
		SeasonNumber:   pgtype.Int4{Int32: 0, Valid: true},
		EpisodeNumber:  pgtype.Int4{Int32: 1, Valid: true},
		AbsoluteNumber: pgtype.Int4{Valid: false},
		AirDate:        pgtype.Date{Time: time.Date(2009, 2, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	})

	return subSvc, evaluator, queries, adminActor, rootID, mediaID
}

func TestTVEpisodeUnAiredAndSpecialsExclusion(t *testing.T) {
	subSvc, evaluator, _, adminActor, rootID, mediaID := setupTVTest(t)
	ctx := context.Background()

	// 1. Default subscription: does NOT include specials (S00)
	sub, wantedItems, err := subSvc.CreateSubscription(ctx, adminActor, subscription.CreateSubscriptionCommand{
		MediaID:         mediaID,
		RootID:          rootID,
		ScopeType:       "all",
		IncludeSpecials: false,
	})
	if err != nil {
		t.Fatalf("create TV sub failed: %v", err)
	}

	// S00E01 must be excluded, only S01E01 and S01E02 expanded!
	if len(wantedItems) != 2 {
		t.Fatalf("expected 2 wanted items (excluding specials), got %d", len(wantedItems))
	}

	// 2. Future un-aired episode check: S01E02 is evaluated as future_air_date
	var futureWantedID uuid.UUID
	for _, wi := range wantedItems {
		// find S01E02
		rep, err := evaluator.EvaluateWantedItem(ctx, wi.ID, quality.DefaultPolicy())
		if err != nil {
			t.Fatalf("eval failed: %v", err)
		}
		if rep.WaitReason == "future_air_date" {
			futureWantedID = wi.ID
		}
	}

	if futureWantedID == uuid.Nil {
		t.Fatal("expected future un-aired episode to have wait_reason=future_air_date")
	}

	// 3. Evolving scope: updating TV scope to include specials (S00)
	updatedSub, newWanted, err := subSvc.UpdateTVScope(ctx, adminActor, sub.ID, "all", nil, true, sub.IntentVersion, "scope-include-specials")
	if err != nil {
		t.Fatalf("update scope failed: %v", err)
	}
	if updatedSub.IntentVersion != 2 {
		t.Fatalf("expected intent_version bumped to 2: %d", updatedSub.IntentVersion)
	}
	if len(newWanted) != 3 {
		t.Fatalf("expected 3 wanted items after including specials, got %d", len(newWanted))
	}

	// 4. Parse episode key domain check
	s, e, isSpecial, err := media.ParseEpisodeKey("S00E01")
	if err != nil || s != 0 || e != 1 || !isSpecial {
		t.Fatalf("expected S00E01 parsed as special: s=%d, e=%d, isSpecial=%v", s, e, isSpecial)
	}
}
