package tmdb_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTVSeasonMetadataDatesUnknownAndRedirectCredentials(t *testing.T) {
	var forwarded atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		_, _ = io.WriteString(w, `{"season_number":1,"episodes":[]}`)
	}))
	defer trap.Close()
	for _, name := range []string{"v3", "v4", "invalid-date", "missing-episodes", "redirect", "oversized"} {
		t.Run(name, func(t *testing.T) {
			key := "fixture-api-key"
			if name == "v4" {
				key = "ey" + strings.Repeat("x", 90)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "v4" {
					if r.Header.Get("Authorization") != "Bearer "+key || r.URL.Query().Get("api_key") != "" {
						t.Error("v4 auth contract")
					}
				} else if r.URL.Query().Get("api_key") != key {
					t.Error("v3 query auth")
				}
				switch name {
				case "redirect":
					http.Redirect(w, r, trap.URL, http.StatusFound)
				case "oversized":
					_, _ = io.WriteString(w, strings.Repeat(" ", 5<<20)+`{}`)
				case "missing-episodes":
					_, _ = io.WriteString(w, `{"season_number":1}`)
				case "invalid-date":
					_, _ = io.WriteString(w, `{"season_number":1,"episodes":[{"id":1001,"season_number":1,"episode_number":1,"air_date":"not-a-date"}]}`)
				default:
					_, _ = io.WriteString(w, `{"season_number":1,"episodes":[{"id":1001,"season_number":1,"episode_number":1,"air_date":"2026-10-10"},{"id":1002,"season_number":1,"episode_number":2,"air_date":null}]}`)
				}
			}))
			defer server.Close()
			client := tmdb.NewClient(key, tmdb.WithBaseURL(server.URL))
			result, e := client.GetTVSeasonMetadata(context.Background(), 100, 1, "")
			if name == "v3" || name == "v4" {
				if e != nil || result == nil || len(result.Episodes) != 2 || result.Episodes[0].TMDBEpisodeID != 1001 || result.Episodes[1].TMDBEpisodeID != 1002 || result.Episodes[0].AirDate == nil || result.Episodes[1].AirDate != nil || result.Episodes[0].AbsoluteNumber != nil {
					t.Fatal("unknown dates/absolute fabricated", result, e)
				}
			} else if !errors.Is(e, ports.ErrBadResponse) {
				t.Fatal("invalid/redirect payload accepted", e)
			}
		})
	}
	if forwarded.Load() != 0 {
		t.Fatal("credentials followed redirect", forwarded.Load())
	}
}
