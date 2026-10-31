package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestClassificationOriginsFromDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/7":
			_, _ = io.WriteString(w, `{"id":7,"title":"Film","overview":"Details","original_language":"ja","production_countries":[{"iso_3166_1":"JP"}],"genres":[{"name":"Animation"}]}`)
		case "/movie/8":
			_, _ = io.WriteString(w, `{"id":8,"title":"Invalid","original_language":"JP","production_countries":[{"iso_3166_1":"JP"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
client := NewClient("fixture", WithBaseURL(server.URL), WithClock(clock.New()))
	got, err := client.GetMovieDetails(context.Background(), 7, "en-US")
	if err != nil || got.OriginalLanguage != "ja" || len(got.Countries) != 1 || got.Countries[0] != "JP" {
		t.Fatal(got, err)
	}
	got.Countries[0] = "US"
	cached, err := client.GetMovieDetails(context.Background(), 7, "en-US")
	if err != nil || cached.Countries[0] != "JP" {
		t.Fatal("caller mutated cached classification facts", cached, err)
	}
	if _, err = client.GetMovieDetails(context.Background(), 8, "en-US"); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("invalid origin accepted", err)
	}
	tv, err := decodeCatalogTVDetails([]byte(`{"id":9,"name":"Series","original_language":"ko","origin_country":["KR"],"seasons":[]}`), 9)
	if err != nil || tv.OriginalLanguage != "ko" || len(tv.Countries) != 1 || tv.Countries[0] != "KR" {
		t.Fatal(tv, err)
	}
	for _, raw := range []string{`{"id":9,"name":"Series","origin_country":["KR","KR"],"seasons":[]}`, `{"id":9,"name":"Series","origin_country":["kr"],"seasons":[]}`} {
		if _, err = decodeCatalogTVDetails([]byte(raw), 9); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatal("invalid TV origin accepted", err)
		}
	}
}
