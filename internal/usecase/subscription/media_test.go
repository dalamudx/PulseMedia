package subscription_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func TestEnsureMediaAtomicAndConcurrent(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, migrationsDir); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	svc := newIntentService(pool, idgen.New())

	// Inserting one episode then encountering an invalid season must not leave media or identity.
	_, err = svc.EnsureMedia(ctx, "tmdb.tv", "failed", "Partial", "tv", nil, []subscription.SeasonEpisodes{{Season: 1, Count: 2}, {Season: -1, Count: 1}})
	if err == nil {
		t.Fatal("invalid second season should fail")
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media WHERE title = 'Partial'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("media not rolled back: count=%d err=%v", n, err)
	}

	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := svc.EnsureMedia(ctx, "tmdb.movie", "550", "Fight Club", "movie", nil, nil)
			ids <- id
			errors <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errors)
	var first uuid.UUID
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent ensure: %v", err)
		}
	}
	for id := range ids {
		if id == uuid.Nil {
			t.Fatal("missing media ID")
		}
		if first == uuid.Nil {
			first = id
		} else if first != id {
			t.Fatalf("identity forked: %v vs %v", first, id)
		}
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_unit WHERE media_id = $1", first).Scan(&n); err != nil || n != 1 {
		t.Fatalf("units=%d err=%v", n, err)
	}
}
