package subscription_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func TestEvaluateOwnershipCrossRootAndDeletedReMissing(t *testing.T) {
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
	subSvc := newIntentService(pool, gen)
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	queries := postgres.Queries(pool)

	// Create user
	userID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{
		ID:           userID,
		Username:     "testuser",
		PasswordHash: "hash",
		Role:         ports.RoleMember,
		IsActive:     true,
	})
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: userID.String(), Name: "testuser", Role: ports.RoleMember}

	// Create Target Root A (/data/movies/a) with baseline completed
	rootAID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootAID,
		Path:      "/data/movies/a",
		MediaType: "movie",
		Name:      "Root A",
	})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootAID)

	// Create Archive Root B (/data/movies/b) with baseline completed
	rootBID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootBID,
		Path:      "/data/movies/b",
		MediaType: "movie",
		Name:      "Root B",
	})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootBID)

	// Create Movie and unit
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "The Matrix", MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})

	// Create subscription targeting Root A
	sub, wantedItems, err := subSvc.CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{
		MediaID:   mediaID,
		RootID:    rootAID,
		ScopeType: "all",
	})
	if err != nil || len(wantedItems) != 1 {
		t.Fatalf("create sub failed: %v", err)
	}
	wantedID := wantedItems[0].ID

	policy := quality.DefaultPolicy()

	// 1. Initially missing in Root A
	rep1, err := evaluator.EvaluateWantedItem(ctx, wantedID, policy)
	if err != nil {
		t.Fatalf("eval 1 failed: %v", err)
	}
	if rep1.IsSatisfied || rep1.WaitReason != "missing" {
		t.Fatalf("expected missing initially: %+v", rep1)
	}

	// 2. Put file in Root B (Cross-Root Isolation! File in Root B must NOT satisfy subscription for Root A!)
	fileBID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              fileBID,
		RootID:          rootBID,
		RelPath:         "Matrix.mkv",
		SizeBytes:       5000000000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: fileBID, UnitID: unitID})

	rep2, err := evaluator.EvaluateWantedItem(ctx, wantedID, policy)
	if err != nil {
		t.Fatalf("eval 2 failed: %v", err)
	}
	if rep2.IsSatisfied {
		t.Fatal("cross-root invariant violated: file in Root B satisfied subscription for Root A")
	}

	// 3. Put file in Root A (Target root): now satisfies!
	fileAID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              fileAID,
		RootID:          rootAID,
		RelPath:         "The Matrix (1999)/The Matrix.mkv",
		SizeBytes:       8000000000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 1,
	})
	_ = queries.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: fileAID, UnitID: unitID})

	// Add quality fact (1080p Bluray)
	factID, _ := uuid.NewV7()
	_, _ = queries.CreateUnitFact(ctx, db.CreateUnitFactParams{
		ID:             factID,
		UnitID:         unitID,
		Stage:          "imported",
		SourceFileID:   pgtype.UUID{Bytes: fileAID, Valid: true},
		RootID:         pgtype.UUID{Bytes: rootAID, Valid: true},
		QualityProfile: []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2"}`),
	})

	rep3, err := evaluator.EvaluateWantedItem(ctx, wantedID, policy)
	if err != nil {
		t.Fatalf("eval 3 failed: %v", err)
	}
	if !rep3.IsSatisfied || *rep3.FactStage != "imported" || rep3.Score <= 0 {
		t.Fatalf("expected satisfied in Root A with score: %+v", rep3)
	}

	// 4. File is DELETED from Root A (marked retired_at): "删除可再次缺失"
	_ = queries.RetireFileByPath(ctx, db.RetireFileByPathParams{
		RootID:  rootAID,
		RelPath: "The Matrix (1999)/The Matrix.mkv",
	})

	rep4, err := evaluator.EvaluateWantedItem(ctx, wantedID, policy)
	if err != nil {
		t.Fatalf("eval 4 failed: %v", err)
	}
	if rep4.IsSatisfied || rep4.WaitReason != "missing" {
		t.Fatalf("deleted file re-missing invariant violated: expected missing after file retired: %+v", rep4)
	}
	_ = sub
}
