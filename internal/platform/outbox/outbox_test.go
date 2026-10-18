package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

const migrationsDir = "../../../migrations"

func TestOutboxConsumerIsolationAndOrdering(t *testing.T) {
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
	c := clock.New()
	ob := outbox.New(pool, gen, c)
	queries := postgres.Queries(pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("failed to begin tx: %v", err)
	}
	qtx := queries.WithTx(tx)

	// 1. Publish Event 1 (seq=1) and Event 2 (seq=2) for Aggregate "movie:1" to "notify" and "jellyfin"
	evt1, err := ob.Publish(ctx, qtx, "movie", "movie:1", "movie.downloaded", map[string]string{"title": "Movie 1"}, []string{"notify", "jellyfin"})
	if err != nil {
		t.Fatalf("publish evt1 failed: %v", err)
	}
	if evt1.Seq != 1 {
		t.Fatalf("expected seq 1 for evt1, got %d", evt1.Seq)
	}

	evt2, err := ob.Publish(ctx, qtx, "movie", "movie:1", "movie.imported", map[string]string{"title": "Movie 1"}, []string{"notify", "jellyfin"})
	if err != nil {
		t.Fatalf("publish evt2 failed: %v", err)
	}
	if evt2.Seq != 2 {
		t.Fatalf("expected seq 2 for evt2, got %d", evt2.Seq)
	}

	// Also publish Event for Aggregate "movie:2" (independent aggregate)
	evtOther, err := ob.Publish(ctx, qtx, "movie", "movie:2", "movie.imported", map[string]string{"title": "Movie 2"}, []string{"notify"})
	if err != nil {
		t.Fatalf("publish other failed: %v", err)
	}
	if evtOther.Seq != 1 {
		t.Fatalf("expected seq 1 for new aggregate, got %d", evtOther.Seq)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	// 2. Consumer "jellyfin" claims its first delivery (must be seq=1)
	jfDelivery1, err := ob.Claim(ctx, "jellyfin", "jf_worker_1", 10*time.Second)
	if err != nil || jfDelivery1 == nil {
		t.Fatalf("jellyfin failed to claim: %v", err)
	}
	if jfDelivery1.Seq != 1 {
		t.Fatalf("expected seq 1 for first delivery, got %d", jfDelivery1.Seq)
	}

	// Consumer "jellyfin" CANNOT claim seq=2 while seq=1 is uncompleted! (Ordered delivery)
	jfDelivery2, err := ob.Claim(ctx, "jellyfin", "jf_worker_2", 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error on second claim: %v", err)
	}
	if jfDelivery2 != nil {
		t.Fatalf("seq=2 should not be claimable while seq=1 is in flight: got %+v", jfDelivery2)
	}

	// 3. Consumer "notify" is completely independent: claims and finishes its own deliveries
	notifyD1, err := ob.Claim(ctx, "notify", "notify_worker_1", 10*time.Second)
	if err != nil || notifyD1 == nil {
		t.Fatalf("notify claim failed: %v", err)
	}

	// Finish jellyfin seq=1
	if err := ob.Finish(ctx, jfDelivery1.ID, "jf_worker_1", jfDelivery1.LeaseToken); err != nil {
		t.Fatalf("failed to finish jf delivery 1: %v", err)
	}

	// Now jellyfin seq=2 is immediately claimable!
	jfDelivery2After, err := ob.Claim(ctx, "jellyfin", "jf_worker_1", 10*time.Second)
	if err != nil || jfDelivery2After == nil {
		t.Fatalf("jellyfin seq=2 should now be claimable: %v", err)
	}
	if jfDelivery2After.Seq != 2 {
		t.Fatalf("expected seq 2, got %d", jfDelivery2After.Seq)
	}
}
