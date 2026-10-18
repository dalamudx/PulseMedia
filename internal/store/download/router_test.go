package download_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

func TestDownloaderRouterDispatch(t *testing.T) {
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
	router := download.NewRouter(queries)

	// 1. Zero clients returns ErrNoDownloadClientAvailable
	_, err = router.SelectClient(ctx, "movie", nil)
	if err == nil {
		t.Fatal("expected error with 0 clients")
	}

	// 2. Add Movie client and TV client
	cMovieID, _ := uuid.NewV7()
	_, _ = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                cMovieID,
		Name:              "qBittorrent Dedicated Movie",
		ClientType:        "qbittorrent",
		Url:               "http://qb-movie:8080",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})

	cTVID, _ := uuid.NewV7()
	_, _ = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                cTVID,
		Name:              "Transmission Dedicated TV",
		ClientType:        "transmission",
		Url:               "http://tr-tv:9091",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})

	// 3. Movie routes to Movie client
	selectedMovie, err := router.SelectClient(ctx, "movie", nil)
	if err != nil || selectedMovie.ID != cMovieID {
		t.Fatalf("movie routing failed: %+v", selectedMovie)
	}

	// 4. TV routes to TV client
	selectedTV, err := router.SelectClient(ctx, "tv", nil)
	if err != nil || selectedTV.ID != cTVID {
		t.Fatalf("tv routing failed: %+v", selectedTV)
	}
}
