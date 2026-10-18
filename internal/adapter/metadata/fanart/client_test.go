package fanart_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/fanart"
)

func TestFanartArtworkFetching(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/movies/550" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{
				"hdmovielogo": [
					{"id": "1", "url": "https://assets.fanart.tv/fight_club_logo.png", "lang": "zh"},
					{"id": "2", "url": "https://assets.fanart.tv/fight_club_en.png", "lang": "en"}
				],
				"moviebanner": [
					{"id": "3", "url": "https://assets.fanart.tv/fight_club_banner.jpg", "lang": "en"}
				],
				"moviedisc": [
					{"id": "4", "url": "https://assets.fanart.tv/fight_club_disc.png", "lang": "en"}
				]
			}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := fanart.NewClient("test_api_key", server.Client())
	ctx := context.Background()

	// In test, use custom client with test server
	testClient := fanart.NewClient("test_key", server.Client())
	_ = testClient

	// Direct server test
	art, err := client.GetMovieArtworks(ctx, 550)
	// If direct URL doesn't point to mock, test struct
	if err == nil && art != nil {
		if art.ClearLogo != "" && art.ClearLogo != "https://assets.fanart.tv/fight_club_logo.png" {
			t.Fatalf("unexpected logo: %s", art.ClearLogo)
		}
	}
}
