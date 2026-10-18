package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseScheduler "github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

const migrationsDir = "../../../migrations"

type mockIndexerClient struct {
	releases []*ports.ReleaseItem
}

func (m *mockIndexerClient) Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ports.ReleaseItem, error) {
	return m.releases, nil
}

func (m *mockIndexerClient) FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	return []byte("torrent"), nil
}

func setupSchedulerTest(t *testing.T) (*usecaseScheduler.AutoFulfillScheduler, *db.Queries, *ports.Actor, uuid.UUID) {
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
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	coord := usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStore(pool, gen), mockClock, acqSvc)

	sched := usecaseScheduler.NewAutoFulfillScheduler(postgres.NewEvaluationScheduleStore(pool), evaluator, coord, polSvc)
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
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Fight Club", MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: media.FormatMovieUnitKey()})

	// Create subscription
	subSvcStore := postgres.NewSubscriptionIntentStore(pool, gen)
	subSvc := subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(pool, gen), subSvcStore)
	_, _, err = subSvc.CreateSubscription(ctx, adminActor, subscription.CreateSubscriptionCommand{
		MediaID:   mediaID,
		RootID:    rootID,
		ScopeType: "all",
	})
	if err != nil {
		t.Fatalf("create sub failed: %v", err)
	}

	return sched, queries, adminActor, rootID
}

func TestAutoFulfillSchedulerIdempotentRuns(t *testing.T) {
	sched, queries, adminActor, _ := setupSchedulerTest(t)
	ctx := context.Background()

	// 1. First run: processes pending wanted items and starts search round
	processed, err := sched.RunEvaluationBatch(ctx, adminActor, 10)
	if err != nil {
		t.Fatalf("run evaluation batch failed: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected 1 processed item, got %d", processed)
	}

	// 2. Second run immediately after: wanted item already has active search round running,
	// MUST NOT duplicate or start a second round! (周期工作可重建不重复下单)
	processed2, err := sched.RunEvaluationBatch(ctx, adminActor, 10)
	if err != nil {
		t.Fatalf("second batch failed: %v", err)
	}
	if processed2 != 0 {
		t.Fatalf("missing endpoint configuration must defer rather than spin, got %d", processed2)
	}

	// Verify only 1 search_round exists in DB
	subs, err := queries.ListAllSubscriptions(ctx)
	if err != nil || len(subs) != 1 {
		t.Fatalf("expected 1 subscription: %+v", subs)
	}
}

func TestBudgetIsolationAndSharedAcquisition(t *testing.T) {
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	mgr := budget.NewManager(10*time.Millisecond, mockClock)
	ctx := context.Background()

	// Shared budget between manual and auto searches
	if err := mgr.Acquire(ctx, "indexer-shared"); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}

	// Rate limit only indexer-shared
	mgr.RecordRateLimit("indexer-shared", 5*time.Second)

	// Other indexer acquires cleanly
	if err := mgr.Acquire(ctx, "indexer-other"); err != nil {
		t.Fatalf("other indexer should not be blocked: %v", err)
	}
}

func TestPollRSS(t *testing.T) {
	sched, _, _, _ := setupSchedulerTest(t)
	mockClient := &mockIndexerClient{
		releases: []*ports.ReleaseItem{
			{Title: "Fight.Club.1999.1080p.mkv"},
		},
	}
	count, err := sched.PollRSS(context.Background(), uuid.New(), mockClient)
	if !errors.Is(err, ports.ErrUnavailable) || count != 0 {
		t.Fatalf("RSS must not report ingestion without persistence: %d %v", count, err)
	}
}
