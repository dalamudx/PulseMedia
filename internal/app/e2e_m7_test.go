package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/douban"
	transfer "github.com/dalamudx/PulseMedia/internal/adapter/transferlegacy"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
)

const migrationsDir = "../../migrations"

func TestFullM7EcosystemIntegration(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	tempDir, err := os.MkdirTemp("", "pulsemedia-e2e-m7-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// 1. Test Transmission Downloader RPC Integration
	trSession := "tr-sess-xyz"
	trServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != trSession {
			w.Header().Set("X-Transmission-Session-Id", trSession)
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"result": "success", "arguments": {"version": "4.0.5"}}`)
	}))
	defer trServer.Close()

	trClient, err := transmission.NewClient(trServer.URL, "admin", "admin", trServer.Client())
	if err != nil {
		t.Fatalf("failed to create transmission client: %v", err)
	}
	if err := trClient.Test(ctx); err != nil {
		t.Fatalf("transmission test failed: %v", err)
	}

	// 2. Test Douban Metadata Provider Integration
	doubanServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"subject_collection_items": [
				{
					"id": "26752088",
					"title": "我不是药神",
					"year": "2018",
					"rating": {"value": 9.0},
					"pic": {"normal": "https://img.doubanio.com/p2561305376.jpg"}
				}
			]
		}`)
	}))
	defer doubanServer.Close()

	doubanClient := douban.NewClient(doubanServer.URL, doubanServer.Client(), douban.WithCredentials(douban.Credentials{APIKey: "e2e-key", Secret: "e2e-secret"}))
	hotMovies, err := doubanClient.GetRanking(ctx, ports.DoubanListHotMovie, 0, 20)
	if err != nil || len(hotMovies) != 1 || hotMovies[0].Title != "我不是药神" {
		t.Fatalf("douban get hot movies failed: %v, %+v", err, hotMovies)
	}

	// 3. Test Kodi/Jellyfin NFO & Poster Scraping Integration
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("JPEG_IMAGE_BYTES"))
	}))
	defer imgServer.Close()

	movieMeta := media.MovieNFOData{
		TMDBID:        550,
		IMDbID:        "tt0137523",
		Title:         "Fight Club",
		OriginalTitle: "Fight Club",
		Year:          1999,
		Plot:          "An underground fight club.",
		Genres:        []string{"Drama"},
	}

	nfoBytes, err := media.GenerateMovieNFO(movieMeta)
	if err != nil || !strings.Contains(string(nfoBytes), `<uniqueid type="tmdb" default="true">550</uniqueid>`) {
		t.Fatalf("generate nfo failed: %v", err)
	}

	targetMovieDir := filepath.Join(tempDir, "Fight Club (1999)")
	err = transfer.ScrapeAndSave(ctx, targetMovieDir, "movie.nfo", nfoBytes, imgServer.URL+"/p.jpg", imgServer.URL+"/f.jpg", imgServer.Client())
	if err != nil {
		t.Fatalf("scrape and save failed: %v", err)
	}

	// Verify files written to disk
	if _, err := os.Stat(filepath.Join(targetMovieDir, "movie.nfo")); err != nil {
		t.Fatal("movie.nfo not created")
	}
	if _, err := os.Stat(filepath.Join(targetMovieDir, "poster.jpg")); err != nil {
		t.Fatal("poster.jpg not created")
	}
	if _, err := os.Stat(filepath.Join(targetMovieDir, "fanart.jpg")); err != nil {
		t.Fatal("fanart.jpg not created")
	}
}
