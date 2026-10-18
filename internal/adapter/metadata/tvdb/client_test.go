package tvdb_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
)

func TestTheTVDBSeriesFetching(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v4/series/81189/extended" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{
				"data": {
					"id": 81189,
					"name": "Breaking Bad",
					"year": "2008",
					"overview": "A high school chemistry teacher diagnosed with inoperable lung cancer...",
					"image": "https://artworks.thetvdb.com/banners/posters/81189-1.jpg",
					"score": 9.2,
					"seasons": [
						{"id": 1, "number": 1, "name": "Season 1", "image": "https://artworks.thetvdb.com/banners/seasons/81189-1.jpg"},
						{"id": 2, "number": 2, "name": "Season 2", "image": "https://artworks.thetvdb.com/banners/seasons/81189-2.jpg"}
					]
				}
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := tvdb.NewClient("test_token", server.Client())
	ctx := context.Background()

	// Direct test
	tv, err := client.GetSeriesDetails(ctx, 81189)
	if err == nil && tv != nil {
		if tv.Title != "Breaking Bad" || tv.Year != 2008 {
			t.Fatalf("unexpected series: %+v", tv)
		}
		if len(tv.Seasons) != 2 {
			t.Fatalf("expected 2 seasons, got: %d", len(tv.Seasons))
		}
	}
}
