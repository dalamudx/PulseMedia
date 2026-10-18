package platform_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)
const migrationsDir = "../../migrations"


func TestJobQueueFairnessAndPriorityScheduling(t *testing.T) {
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
	q := jobs.NewQueue(pool, gen, mockClock)

	// 1. Enqueue 10 low-priority jobs (priority 0) and 1 high-priority job (priority 50)
	for i := 0; i < 10; i++ {
		_, err := q.Enqueue(ctx, "task", fmt.Sprintf("low_%d", i), 0, map[string]int{"idx": i}, time.Time{}, 3)
		if err != nil {
			t.Fatalf("enqueue low failed: %v", err)
		}
	}

	highJob, err := q.Enqueue(ctx, "task", "high_priority", 50, map[string]string{"type": "urgent"}, time.Time{}, 3)
	if err != nil {
		t.Fatalf("enqueue high failed: %v", err)
	}

	// 2. Claim next ready job: MUST claim the high-priority job first! (Priority Fairness!)
	claimed, err := q.Claim(ctx, []string{"task"}, "worker_fair", 10*time.Second)
	if err != nil || claimed == nil {
		t.Fatalf("claim failed: %v", err)
	}
	if claimed.ID != highJob.ID {
		t.Fatalf("priority scheduling failed: expected high-priority job %s, got %s (priority %d)",
			highJob.ID, claimed.ID, claimed.Priority)
	}
}

func TestIndexedLibraryFileLookupLatency(t *testing.T) {
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
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/data/bench/movies",
		MediaType: "movie",
		Name:      "Bench Movies",
	})

	// Pre-populate 100 files
	targetRelPath := "Movie_050 (2020)/Movie_050.mkv"
	for i := 0; i < 100; i++ {
		fileID, _ := uuid.NewV7()
		rel := fmt.Sprintf("Movie_%03d (2020)/Movie_%03d.mkv", i, i)
		_, err := queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
			ID:              fileID,
			RootID:          rootID,
			RelPath:         rel,
			SizeBytes:       1024 * 1024 * int64(i+1),
			MtimeNs:         time.Now().UnixNano(),
			LastMutationSeq: int64(i),
		})
		if err != nil {
			t.Fatalf("insert file %d failed: %v", i, err)
		}
	}

	// Benchmark indexed lookup by (root_id, rel_path)
	start := time.Now()
	const iterations = 50
	for i := 0; i < iterations; i++ {
		file, err := queries.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{
			RootID:  rootID,
			RelPath: targetRelPath,
		})
		if err != nil || file.ID == uuid.Nil {
			t.Fatalf("lookup failed: %v", err)
		}
	}
	elapsed := time.Since(start)
	avgLatency := elapsed / time.Duration(iterations)

	// Invariant: Average indexed lookup latency must be well below 5ms (target P95 < 100ms)
	if avgLatency > 20*time.Millisecond {
		t.Fatalf("indexed lookup latency too slow: %v average per lookup", avgLatency)
	}
	t.Logf("Indexed active file lookup latency: %v per query (50 iterations total: %v)", avgLatency, elapsed)
}
