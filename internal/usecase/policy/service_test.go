package policy_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

const migrationsDir = "../../../migrations"

func TestConcurrentPolicyPublishes(t *testing.T) {
	service, _, q, admin, _ := setupPolicyTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p := quality.DefaultPolicy()
	rules := quality.PolicyRules{CutoffScore: 300, ResolutionOrder: p.ResolutionOrder, SourceOrder: p.SourceOrder}
	type receipt struct {
		revision int64
		err      error
	}
	receipts := make(chan receipt, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			revision, err := service.PublishNewRevision(ctx, admin, "default", rules)
			receipts <- receipt{revision, err}
		}()
	}
	wg.Wait()
	close(receipts)
	seen := map[int64]bool{}
	for value := range receipts {
		if value.err != nil || seen[value.revision] {
			t.Fatalf("nonlinear policy publish: revision=%d duplicate=%t err=%v context=%v", value.revision, seen[value.revision], value.err, ctx.Err())
		}
		seen[value.revision] = true
	}
	current, err := q.GetPolicyByID(ctx, "default")
	if err != nil || current.CurrentRevision != 9 || len(seen) != 8 {
		t.Fatal("revision sequence lost", current, err)
	}
}

func setupPolicyTest(t *testing.T) (*policy.Service, *subscription.Service, *db.Queries, *ports.Actor, *ports.Actor) {
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
	polSvc := policy.NewService(postgres.NewPolicyStore(pool, gen))
	subSvcStore := postgres.NewSubscriptionIntentStore(pool, gen)
	subSvc := subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(pool, gen), subSvcStore)
	queries := postgres.Queries(pool)

	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.New().String(), Name: "admin", Role: ports.RoleAdmin}
	memberActor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.New().String(), Name: "member", Role: ports.RoleMember}

	for _, actor := range []*ports.Actor{adminActor, memberActor} {
		if _, err := queries.CreateUser(context.Background(), db.CreateUserParams{ID: uuid.MustParse(actor.ID), Username: actor.Name, PasswordHash: "synthetic-policy-fixture", Role: actor.Role, IsActive: true}); err != nil {
			t.Fatal(err)
		}
	}
	return polSvc, subSvc, queries, adminActor, memberActor
}

func TestPolicyImmutableRevisionAndRollback(t *testing.T) {
	polSvc, subSvc, queries, adminActor, memberActor := setupPolicyTest(t)
	ctx := context.Background()

	// Initial default policy is at revision 1
	rev1Rules, err := polSvc.GetRevision(ctx, "default", 1)
	if err != nil {
		t.Fatalf("load revision 1 failed: %v", err)
	}
	if rev1Rules.CutoffScore != 300 {
		t.Fatalf("expected revision 1 cutoff 300, got %d", rev1Rules.CutoffScore)
	}

	// 1. Member editing policy is rejected with ErrForbidden
	newRules := *rev1Rules
	newRules.CutoffScore = 400
	_, err = polSvc.PublishNewRevision(ctx, memberActor, "default", newRules)
	if err == nil {
		t.Fatal("expected member to be forbidden from publishing policy revision")
	}

	// 2. Admin publishes new revision -> version increments monotonically to 2
	rev2, err := polSvc.PublishNewRevision(ctx, adminActor, "default", newRules)
	if err != nil {
		t.Fatalf("admin publish revision failed: %v", err)
	}
	if rev2 != 2 {
		t.Fatalf("expected monotonic revision 2, got %d", rev2)
	}

	// Verify revision 1 rules remain completely unchanged (Immutable!)
	rev1Check, _ := polSvc.GetRevision(ctx, "default", 1)
	if rev1Check.CutoffScore != 300 {
		t.Fatal("revision 1 must be immutable and unchanged")
	}

	// 3. Rollback to revision 1 creates a new revision 3 with revision 1 rules
	rev3, err := polSvc.RollbackToRevision(ctx, adminActor, "default", 1)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if rev3 != 3 {
		t.Fatalf("expected monotonic revision 3 for rollback, got %d", rev3)
	}

	rev3Rules, _ := polSvc.GetRevision(ctx, "default", 3)
	if rev3Rules.CutoffScore != 300 {
		t.Fatalf("expected rolled back rules with cutoff 300, got %d", rev3Rules.CutoffScore)
	}

	// 4. Preview explanation test
	candProfile := quality.Profile{Resolution: quality.Res2160p, Source: quality.SourceRemux, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion}
	previewValid := polSvc.Preview(*rev1Rules, "Fight.Club.1999.2160p.Remux", candProfile)
	if !previewValid.Passed || !previewValid.MeetsCutoff {
		t.Fatalf("expected candidate to pass and meet cutoff: %+v", previewValid)
	}

	// Candidate with reject term (CAM) is rejected
	previewCAM := polSvc.Preview(*rev1Rules, "Fight.Club.1999.HDCAM-GROUP", candProfile)
	if previewCAM.Passed || previewCAM.RejectedReason == "" {
		t.Fatalf("expected CAM candidate to be rejected: %+v", previewCAM)
	}

	_ = subSvc
	_ = queries
}
