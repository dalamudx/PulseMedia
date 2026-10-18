package tmdb_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTMDBSearchAndDetailsWithFallback(t *testing.T) {
	// Mock TMDB API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 1. Search movie
		if r.URL.Path == "/search/movie" {
			q := r.URL.Query().Get("query")
			if q == "Fight Club" {
				_, _ = fmt.Fprint(w, `{
					"results": [
						{
							"id": 550,
							"title": "搏击俱乐部",
							"original_title": "Fight Club",
							"release_date": "1999-10-15",
							"overview": "一个饱受失眠折磨的白领...",
							"poster_path": "/pB8BM7pdSp6B6Ih7QZ4DrQ3PmJK.jpg"
						}
					]
				}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"results": []}`)
			return
		}

		// 2. Movie details (zh-CN returns empty overview to test en-US fallback)
		if r.URL.Path == "/movie/550" {
			lang := r.URL.Query().Get("language")
			if lang == "zh-CN" {
				w.Header().Set("ETag", `"etag-550-zh"`)
				_, _ = fmt.Fprint(w, `{
					"id": 550,
					"imdb_id": "tt0137523",
					"title": "搏击俱乐部",
					"original_title": "Fight Club",
					"release_date": "1999-10-15",
					"overview": "",
					"genres": [{"name": "剧情"}]
				}`)
				return
			}
			if lang == "en-US" {
				_, _ = fmt.Fprint(w, `{
					"id": 550,
					"imdb_id": "tt0137523",
					"title": "Fight Club",
					"original_title": "Fight Club",
					"release_date": "1999-10-15",
					"overview": "A ticking-time-bomb insomniac...",
					"genres": [{"name": "Drama"}]
				}`)
				return
			}
		}

		// 3. Find by external ID
		if r.URL.Path == "/find/tt0137523" {
			_, _ = fmt.Fprint(w, `{
				"movie_results": [{"id": 550}]
			}`)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	mockClock := clock.NewMock(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	client := tmdb.NewClient("test-api-key",
		tmdb.WithBaseURL(server.URL),
		tmdb.WithClock(mockClock),
	)

	ctx := context.Background()

	// 1. Search movies
	movies, err := client.SearchMovies(ctx, "Fight Club", 1999)
	if err != nil {
		t.Fatalf("search movies failed: %v", err)
	}
	if len(movies) != 1 || movies[0].TMDBID != 550 {
		t.Fatalf("unexpected search results: %+v", movies)
	}

	// 2. Details with English fallback for overview
	details, err := client.GetMovieDetails(ctx, 550, "zh-CN")
	if err != nil {
		t.Fatalf("get details failed: %v", err)
	}
	if details.TMDBID != 550 || details.IMDbID != "tt0137523" || details.MediaType != "movie" {
		t.Fatalf("unexpected details: %+v", details)
	}
	// Overview fell back to English because zh-CN was empty
	if details.Overview != "A ticking-time-bomb insomniac..." {
		t.Fatalf("expected English overview fallback, got: %q", details.Overview)
	}

	// 3. Find by IMDb external ID
	found, err := client.FindMovieByExternalID(ctx, "imdb", "tt0137523")
	if err != nil {
		t.Fatalf("find by external ID failed: %v", err)
	}
	if found.TMDBID != 550 {
		t.Fatalf("expected TMDB ID 550 from IMDb find, got: %d", found.TMDBID)
	}
}

func TestTMDBErrorClassification(t *testing.T) {
	// Server returning 429 Rate Limit
	rateLimitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer rateLimitServer.Close()

	client := tmdb.NewClient("key", tmdb.WithBaseURL(rateLimitServer.URL))
	ctx := context.Background()

	_, err := client.GetMovieDetails(ctx, 123, "zh-CN")
	rle, ok := ports.IsRateLimited(err)
	if !ok || rle.RetryAfter != 12*time.Second {
		t.Fatalf("expected RateLimitError with RetryAfter 12s, got: %v", err)
	}

	// Server returning 401 Unauthorized
	unauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthServer.Close()

	unauthClient := tmdb.NewClient("bad_key", tmdb.WithBaseURL(unauthServer.URL))
	_, err = unauthClient.GetMovieDetails(ctx, 123, "zh-CN")
	if !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatalf("expected ErrAuthRequired on 401, got: %v", err)
	}
}
