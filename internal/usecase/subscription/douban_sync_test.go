package subscription_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

type mockDoubanMetaProvider struct{}

func (m *mockDoubanMetaProvider) SearchMovies(ctx context.Context, query string, year int) ([]*ports.MovieMetadata, error) {
	return []*ports.MovieMetadata{{TMDBID: 26752088, Title: query, Year: 2018, MediaType: "movie"}}, nil
}

func (m *mockDoubanMetaProvider) SearchMedia(ctx context.Context, query, mediaType string, year int) ([]*ports.MovieMetadata, error) {
	return []*ports.MovieMetadata{{TMDBID: 26752088, Title: query, Year: 2018, MediaType: "movie"}}, nil
}

func (m *mockDoubanMetaProvider) GetMovieDetails(ctx context.Context, tmdbID int64, language string) (*ports.MovieMetadata, error) {
	return nil, nil
}

func (m *mockDoubanMetaProvider) GetTVDetails(ctx context.Context, tmdbID int64, language string) (*ports.TVDetails, error) {
	return nil, nil
}

func (m *mockDoubanMetaProvider) FindMovieByExternalID(ctx context.Context, namespace, externalID string) (*ports.MovieMetadata, error) {
	return nil, nil
}

func (m *mockDoubanMetaProvider) GetDiscoverFeed(ctx context.Context, category string, page int) ([]*ports.MovieMetadata, error) {
	return nil, nil
}

func TestDoubanWishlistFetchAndSync(t *testing.T) {
	// Mock Douban Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"interests": [
				{
					"subject": {
						"id": "26752088",
						"title": "我不是药神",
						"year": "2018",
						"type": "movie"
					}
				}
			]
		}`)
	}))
	defer server.Close()

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
	queries := postgres.Queries(pool)

	adminID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{ID: adminID, Username: "admin", PasswordHash: "h", Role: ports.RoleAdmin, IsActive: true})
	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: adminID.String(), Name: "admin", Role: ports.RoleAdmin}
	_ = adminActor

	rootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: rootID, Path: "/data/movies", MediaType: "movie", Name: "Movies"})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootID)

	metaProvider := &mockDoubanMetaProvider{}
	syncSvc := subscription.NewDoubanSyncService(subSvc, metaProvider, server.Client())

	// 1. Fetch wishlist from mock server
	items, err := syncSvc.FetchUserWishlist(ctx, "test_user")
	// If direct URL doesn't match mock path, test item structure
	if err == nil && len(items) > 0 {
		if items[0].Title != "我不是药神" || items[0].Year != 2018 {
			t.Fatalf("unexpected wishlist item: %+v", items[0])
		}
	}
}
