package media_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	usecaseMedia "github.com/dalamudx/PulseMedia/internal/usecase/media"
)

const migrationsDir = "../../../migrations"

type mockMetadataProvider struct {
	movies map[string]*ports.MovieMetadata
}

func (m *mockMetadataProvider) SearchMovies(ctx context.Context, query string, year int) ([]*ports.MovieMetadata, error) {
	if meta, ok := m.movies[query]; ok {
		return []*ports.MovieMetadata{meta}, nil
	}
	return nil, nil
}

func (m *mockMetadataProvider) SearchMovieIdentityCandidates(ctx context.Context, query string, year int) (ports.MovieIdentitySearch, error) {
	results, err := m.SearchMovies(ctx, query, year)
	return ports.MovieIdentitySearch{Candidates: results, Complete: true}, err
}
func (m *mockMetadataProvider) GetMovieDetails(ctx context.Context, tmdbID int64, language string) (*ports.MovieMetadata, error) {
	for _, meta := range m.movies {
		if meta.TMDBID == tmdbID {
			return meta, nil
		}
	}
	return nil, ports.ErrNotFound
}

func (m *mockMetadataProvider) FindMovieByExternalID(ctx context.Context, namespace, externalID string) (*ports.MovieMetadata, error) {
	for _, meta := range m.movies {
		if namespace == "imdb" && meta.IMDbID == externalID {
			return meta, nil
		}
	}
	return nil, ports.ErrNotFound
}

func (m *mockMetadataProvider) GetDiscoverFeed(ctx context.Context, category string, page int) ([]*ports.MovieMetadata, error) {
	return nil, nil
}

func (m *mockMetadataProvider) SearchMedia(ctx context.Context, query, mediaType string, year int) ([]*ports.MovieMetadata, error) {
	return m.SearchMovies(ctx, query, year)
}

func (m *mockMetadataProvider) GetTVDetails(ctx context.Context, tmdbID int64, language string) (*ports.TVDetails, error) {
	return nil, nil
}

func TestMovieMatcherAndReviewFlagging(t *testing.T) {
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
	mockProvider := &mockMetadataProvider{
		movies: map[string]*ports.MovieMetadata{
			"Fight Club": {
				TMDBID:        550,
				IMDbID:        "tt0137523",
				Title:         "Fight Club",
				OriginalTitle: "Fight Club",
				Year:          1999,
			},
		},
	}

	matcher := usecaseMedia.NewMatcher(postgres.NewMovieLinkStore(pool, gen), mockProvider)
	queries := postgres.Queries(pool)

	rootID, _ := uuid.NewV7()
	root, err := queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{
		ID:        rootID,
		Path:      "/media/movies",
		MediaType: "movie",
		Name:      "Movies",
	})
	if err != nil {
		t.Fatalf("failed to create root: %v", err)
	}

	// 1. High-confidence movie file matching
	cleanRel := "Fight.Club.1999.1080p.BluRay.x264.mkv"
	fileID, _ := uuid.NewV7()
	_, err = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              fileID,
		RootID:          root.ID,
		RelPath:         cleanRel,
		SizeBytes:       8000000000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 0,
	})
	if err != nil {
		t.Fatalf("failed to insert file: %v", err)
	}

	res, err := matcher.MatchAndLinkFile(ctx, root.ID, cleanRel)
	if err != nil {
		t.Fatalf("matching failed: %v", err)
	}
	if res.RequiresReview {
		t.Fatal("expected high confidence match to not require review")
	}
	if res.Media.Title != "Fight Club" || res.Unit.UnitKey != "movie" {
		t.Fatalf("unexpected matched media: %+v", res.Media)
	}

	// Verify imported fact was created
	facts, err := queries.ListActiveFactsForUnit(ctx, res.Unit.ID)
	if err != nil || len(facts) != 1 {
		t.Fatalf("expected 1 fact for unit, got %d (err: %v)", len(facts), err)
	}
	if facts[0].Stage != "imported" {
		t.Fatalf("expected imported fact, got %s", facts[0].Stage)
	}

	// 2. Low-confidence file (no year, obscure name) requires review and is NOT auto-bound
	lowConfPath := "Unknown_Home_Recording.avi"
	file2ID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryFile(ctx, db.CreateLibraryFileParams{
		ID:              file2ID,
		RootID:          root.ID,
		RelPath:         lowConfPath,
		SizeBytes:       500000,
		MtimeNs:         time.Now().UnixNano(),
		LastMutationSeq: 0,
	})

	resLow, err := matcher.MatchAndLinkFile(ctx, root.ID, lowConfPath)
	if !errors.Is(err, usecaseMedia.ErrLowConfidence) {
		t.Fatalf("expected ErrLowConfidence, got: %v", err)
	}
	if !resLow.RequiresReview {
		t.Fatal("expected low confidence file to require review")
	}
}
