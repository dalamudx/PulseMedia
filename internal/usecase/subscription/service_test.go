package subscription_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

const migrationsDir = "../../../migrations"

func setupSubscriptionTest(t *testing.T) (*subscription.Service, *db.Queries, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
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
	svc := newIntentService(pool, gen)
	queries := postgres.Queries(pool)

	// Create member1 and member2
	member1ID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{
		ID:           member1ID,
		Username:     "member1",
		PasswordHash: "hash",
		Role:         ports.RoleMember,
		IsActive:     true,
	})

	member2ID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{
		ID:           member2ID,
		Username:     "member2",
		PasswordHash: "hash",
		Role:         ports.RoleMember,
		IsActive:     true,
	})

	// Create root
	rootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/data/media/tv",
		MediaType: "tv",
		Name:      "TV Shows",
	})

	// Create TV show with 2 episodes
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Breaking Bad", MediaType: "tv"})
	unit1ID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit1ID, MediaID: mediaID, UnitKey: "S01E01"})
	unit2ID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unit2ID, MediaID: mediaID, UnitKey: "S01E02"})

	return svc, queries, member1ID, member2ID, rootID, mediaID, unit1ID
}

func TestSubscriptionLifecycleAndScopeExpansion(t *testing.T) {
	svc, queries, member1ID, member2ID, rootID, mediaID, unit1ID := setupSubscriptionTest(t)
	ctx := context.Background()

	member1Actor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   member1ID.String(),
		Name: "member1",
		Role: ports.RoleMember,
	}

	member2Actor := &ports.Actor{
		Type: ports.ActorTypeUser,
		ID:   member2ID.String(),
		Name: "member2",
		Role: ports.RoleMember,
	}

	// 1. Create subscription with scope='all'
	cmdAll := subscription.CreateSubscriptionCommand{
		MediaID:   mediaID,
		RootID:    rootID,
		ScopeType: "all",
	}

	sub, wantedItems, err := svc.CreateSubscription(ctx, member1Actor, cmdAll)
	if err != nil {
		t.Fatalf("create subscription failed: %v", err)
	}
	if sub.State != "active" || sub.IntentVersion != 1 {
		t.Fatalf("unexpected sub state: %s, version=%d", sub.State, sub.IntentVersion)
	}
	if len(wantedItems) != 2 {
		t.Fatalf("expected 2 wanted items expanded for 2 episodes, got %d", len(wantedItems))
	}
	for _, wi := range wantedItems {
		if wi.IsSkipped {
			t.Fatal("all scope units should not be skipped")
		}
	}

	// 2. Member 1 updates state to paused -> intent_version bumps to 2
	pausedSub, err := svc.UpdateState(ctx, member1Actor, sub.ID, "paused")
	if err != nil {
		t.Fatalf("pause sub failed: %v", err)
	}
	if pausedSub.State != "paused" || pausedSub.IntentVersion != 2 {
		t.Fatalf("expected paused state with intent_version=2: %+v", pausedSub)
	}

	// 3. Member 2 attempts to pause Member 1's subscription: MUST be rejected with ErrForbidden!
	// (Invariant: 对象权限强隔离)
	_, err = svc.UpdateState(ctx, member2Actor, sub.ID, "archived")
	if !errors.Is(err, ports.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-owner member, got: %v", err)
	}

	// 4. Toggle skip on unit 1
	err = svc.SetWantedSkipped(ctx, member1Actor, sub.ID, unit1ID, true)
	if err != nil {
		t.Fatalf("set skipped failed: %v", err)
	}

	items, err := queries.ListWantedItemsForSubscription(ctx, sub.ID)
	if err != nil {
		t.Fatalf("list wanted items failed: %v", err)
	}
	foundSkipped := false
	for _, wi := range items {
		if wi.UnitID == unit1ID && wi.IsSkipped {
			foundSkipped = true
		}
	}
	if !foundSkipped {
		t.Fatal("expected unit 1 to be marked skipped")
	}

	// 5. Invalid state rejected
	_, err = svc.UpdateState(ctx, member1Actor, sub.ID, "invalid_state")
	if !errors.Is(err, subscription.ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState, got: %v", err)
	}
}

func TestSubscriptionScopeSelectedExpansion(t *testing.T) {
	svc, _, member1ID, _, rootID, mediaID, unit1ID := setupSubscriptionTest(t)
	ctx := context.Background()

	member1Actor := &ports.Actor{Type: ports.ActorTypeUser, ID: member1ID.String(), Name: "member1", Role: ports.RoleMember}

	// Create subscription with scope='selected' specifying only unit1
	cmdSelected := subscription.CreateSubscriptionCommand{
		MediaID:         mediaID,
		RootID:          rootID,
		ScopeType:       "selected",
		SelectedUnitIDs: []uuid.UUID{unit1ID},
	}

	sub, wantedItems, err := svc.CreateSubscription(ctx, member1Actor, cmdSelected)
	if err != nil {
		t.Fatalf("create selected sub failed: %v", err)
	}
	if len(wantedItems) != 2 {
		t.Fatalf("expected 2 items, got %d", len(wantedItems))
	}

	for _, wi := range wantedItems {
		if wi.UnitID == unit1ID && wi.IsSkipped {
			t.Fatalf("selected unit %s should not be skipped", wi.UnitID)
		}
		if wi.UnitID != unit1ID && !wi.IsSkipped {
			t.Fatalf("unselected unit %s must be marked skipped", wi.UnitID)
		}
	}
	_ = sub
}
