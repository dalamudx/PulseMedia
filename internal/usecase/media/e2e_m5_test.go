package media_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseScheduler "github.com/dalamudx/PulseMedia/internal/usecase/scheduler"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

type e2eM5MockIndexer struct {
	releases []*ports.ReleaseItem
}

func (m *e2eM5MockIndexer) Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ports.ReleaseItem, error) {
	return m.releases, nil
}

func (m *e2eM5MockIndexer) FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	return []byte("fake-torrent"), nil
}

func TestTVIntentAndRealQueryDoNotAuthorizeUnfrozenAcquisition(t *testing.T) {
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
	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}

	queries := postgres.Queries(pool)

	tempDir, err := os.MkdirTemp("", "pulsemedia-e2e-m5-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	tvDir := filepath.Join(tempDir, "tv_shows")
	downloadsDir := filepath.Join(tempDir, "downloads")
	_ = os.MkdirAll(tvDir, 0755)
	_ = os.MkdirAll(downloadsDir, 0755)
	// Explicit source capability: a task save_path does not authorize a directory.
	if _, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: downloadsDir, MediaType: "movie", Name: "Fixture download source"}); err != nil {
		t.Fatalf("configure source root: %v", err)
	}
	_ = os.WriteFile(filepath.Join(tvDir, ".pulsemedia_root"), []byte("tv_ok"), 0644)

	// 1. Setup Admin Actor
	authSvc := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, mockClock, gen)
	adminUser, _, err := authSvc.SetupAdmin(ctx, "admin", "AdminPass12345!")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: adminUser.ID.String(), Name: adminUser.Username, Role: ports.RoleAdmin}

	// 2. Setup TV Library Root
	tvRootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        tvRootID,
		Path:      tvDir,
		MediaType: "tv",
		Name:      "TV Library",
	})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", tvRootID)

	// 3. Step M5-02: Configure Torznab Indexer Site & Test Connectivity
	searchSvc := usecaseSearch.NewService(postgres.NewSearchExecutionStore(pool), gen, crypto.NewSecretProtector(masterKey), 0)
	searchSvc.SetHealthRepository(postgres.NewIndexerHealthStore(pool, gen))
	mockIndexer := &e2eM5MockIndexer{
		releases: []*ports.ReleaseItem{
			{
				Title:       "Breaking.Bad.S01E01.1080p.BluRay.x264",
				GUID:        "guid-bb-s01e01",
				DownloadURL: "https://mock.tracker/dl/s01e01.torrent",
				SizeBytes:   2500000000,
				Seeders:     42,
			},
		},
	}
	searchSvc.SetClientFactory(func(baseURL, apiKey string) ports.IndexerClient {
		return mockIndexer
	})

	createdIndexer, err := searchSvc.CreateIndexer(ctx, "Prowlarr Main", "http://prowlarr:9696/1/api", "passkey123", []int{2000, 5000})
	if err != nil {
		t.Fatalf("create indexer failed: %v", err)
	}

	alive, latency, msg, err := searchSvc.TestIndexer(ctx, createdIndexer.ID)
	if err != nil || !alive || msg != "ok" {
		t.Fatalf("test indexer failed: alive=%v, latency=%d, msg=%s, err=%v", alive, latency, msg, err)
	}

	// 4. Step M5-01 & M5-04: TMDB ID Driven TV Subscription Creation (Auto-creates media & expands episodes)
	acqSvc := acquisition.NewService(postgres.NewAcquisitionStore(pool, gen, mockClock, acquisition.SelectMovieRound))
	polSvc := policy.NewService(postgres.NewPolicyStore(pool, gen))
	evaluator := subscription.NewEvaluator(postgres.NewWantedProjectionStore(pool))
	coord := usecaseSearch.NewRoundCoordinator(postgres.NewSearchRoundStore(pool, gen), mockClock, acqSvc)
	scheduler := usecaseScheduler.NewAutoFulfillScheduler(postgres.NewEvaluationScheduleStore(pool), evaluator, coord, polSvc)
	subSvcStore := postgres.NewSubscriptionIntentStore(pool, gen)
	subSvc := subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(pool, gen), subSvcStore)

	// Simulate TMDB TV show metadata
	tvTMDBID := int64(1396)
	tvTitle := "Breaking Bad"

	// Auto-create media & units for TV show (as done in our upgraded handler)
	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{
		ID:        mediaID,
		Title:     tvTitle,
		MediaType: "tv",
	})
	_, _ = queries.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{
		ID:         uuid.New(),
		MediaID:    mediaID,
		Namespace:  "tmdb.tv",
		ExternalID: fmt.Sprintf("%d", tvTMDBID),
	})

	// Create S01E01 unit
	s1e1ID, _ := uuid.NewV7()
	_, _ = queries.CreateTVUnit(ctx, db.CreateTVUnitParams{
		ID:            s1e1ID,
		MediaID:       mediaID,
		UnitKey:       "S01E01",
		SeasonNumber:  pgtype.Int4{Int32: 1, Valid: true},
		EpisodeNumber: pgtype.Int4{Int32: 1, Valid: true},
	})

	// Create subscription with scope='all'
	sub, wantedItems, err := subSvc.CreateSubscription(ctx, adminActor, subscription.CreateSubscriptionCommand{
		MediaID:         mediaID,
		RootID:          tvRootID,
		ScopeType:       "all",
		QualityPolicyID: "default",
	})
	if err != nil || len(wantedItems) != 1 {
		t.Fatalf("create TV subscription failed: %v", err)
	}

	// 5. Step M5-03: Aggregate Search Across All Configured Indexers
	candidates, err := searchSvc.AggregateSearch(ctx, tvTitle)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("aggregate search failed: len=%d, err=%v", len(candidates), err)
	}
	cand := candidates[0]
	if cand.Title != "Breaking.Bad.S01E01.1080p.BluRay.x264" || cand.Seeders != 42 {
		t.Fatalf("unexpected candidate returned: %+v", cand)
	}

	// TV intent and one search hit are not a verified full-coverage acquisition.
	_, err = acqSvc.AcquireMovie(ctx, adminActor, acquisition.AcquireMovieCommand{UnitIDs: []uuid.UUID{s1e1ID}, RootID: tvRootID, CandidateID: cand.ID})
	if !errors.Is(err, acquisition.ErrUnsupportedCoverage) {
		t.Fatalf("unfrozen TV acquisition escaped gate: %v", err)
	}
	var reservations, tasks, jobs int
	if e := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM download_task),(SELECT count(*) FROM job WHERE kind='download_submission')").Scan(&reservations, &tasks, &jobs); e != nil || reservations != 0 || tasks != 0 || jobs != 0 {
		t.Fatalf("TV gate created protection/work: %d %d %d %v", reservations, tasks, jobs, e)
	}
	evalRep, err := evaluator.EvaluateWantedItem(ctx, wantedItems[0].ID, quality.DefaultPolicy())
	if err != nil || evalRep.IsSatisfied {
		t.Fatalf("TV intent/search alone manufactured owned fact: %+v %v", evalRep, err)
	}
	_ = sub
	_ = scheduler
}
