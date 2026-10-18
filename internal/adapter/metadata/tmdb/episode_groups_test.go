package tmdb_test

import (
	"context"
	"encoding/json"
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

func TestEpisodeGroupsMembershipRequiredFieldsAndExplicitOrder(t *testing.T) {
	const listing = `{"results":[{"id":"group1","name":"Fixture absolute","type":2,"episode_count":2,"group_count":1}]}`
	const detail = `{"id":"group1","type":2,"episode_count":2,"group_count":1,"groups":[{"id":"part1","name":"Whole series","order":0,"episodes":[{"id":1002,"season_number":2,"episode_number":1,"order":1},{"id":1001,"season_number":1,"episode_number":1,"order":0}]}]}`
	for _, name := range []string{"v3", "v4", "foreign", "missing-results", "duplicate-summary", "unsafe-id", "missing-order", "duplicate-order", "duplicate-id", "canonical-conflict", "missing-canonical", "missing-groups", "count-mismatch", "wrong-id", "wrong-type", "redirect", "oversized", "unknown-season-id", "duplicate-season-id"} {
		t.Run(name, func(t *testing.T) {
			var details, forwarded atomic.Int32
			trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
			defer trap.Close()
			key := "fixture-key"
			if name == "v4" {
				key = "ey" + strings.Repeat("x", 90)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "v4" {
					if r.Header.Get("Authorization") != "Bearer "+key || r.URL.Query().Get("api_key") != "" {
						t.Error("wrong v4 auth")
					}
				} else if r.URL.Query().Get("api_key") != key {
					t.Error("wrong v3 auth")
				}
				if strings.Contains(r.URL.Path, "/season/") {
					body := `{"season_number":1,"episodes":[{"season_number":1,"episode_number":1},{"id":1002,"season_number":1,"episode_number":2}]}`
					if name == "duplicate-season-id" {
						body = strings.Replace(body, `{"season_number":1`, `{"id":1002,"season_number":1`, 1)
					}
					_, _ = io.WriteString(w, body)
					return
				}
				if r.URL.Path == "/tv/100/episode_groups" {
					body := listing
					switch name {
					case "foreign":
						body = `{"results":[]}`
					case "missing-results":
						body = `{}`
					case "duplicate-summary":
						body = strings.Replace(listing, `}]}`, `},{"id":"group1","type":2,"episode_count":2,"group_count":1}]}`, 1)
					case "unsafe-id":
						body = strings.Replace(listing, "group1", "../escape", 1)
					}
					_, _ = io.WriteString(w, body)
					return
				}
				details.Add(1)
				body := detail
				switch name {
				case "missing-order":
					body = strings.Replace(body, `,"order":1`, "", 1)
				case "duplicate-order":
					body = strings.Replace(body, `"order":1`, `"order":0`, 1)
				case "duplicate-id":
					body = strings.Replace(body, `"id":1002`, `"id":1001`, 1)
				case "canonical-conflict":
					body = strings.Replace(body, `"season_number":2`, `"season_number":1`, 1)
				case "missing-canonical":
					body = strings.Replace(body, `"season_number":2,`, "", 1)
				case "missing-groups":
					body = `{"id":"group1","type":2,"episode_count":2,"group_count":1}`
				case "count-mismatch":
					body = strings.Replace(body, `"episode_count":2`, `"episode_count":3`, 1)
				case "wrong-id":
					body = strings.Replace(body, "group1", "group2", 1)
				case "wrong-type":
					body = strings.Replace(body, `"type":2`, `"type":3`, 1)
				case "redirect":
					http.Redirect(w, r, trap.URL, http.StatusFound)
					return
				case "oversized":
					body = strings.Repeat(" ", 5<<20) + body
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			c := tmdb.NewClient(key, tmdb.WithBaseURL(server.URL))
			if strings.Contains(name, "season-id") {
				if _, e := c.GetTVSeasonMetadata(context.Background(), 100, 1, ""); !errors.Is(e, ports.ErrBadResponse) {
					t.Fatal("missing/duplicate actual episode identity accepted", e)
				}
				return
			}
			g, e := c.GetTVEpisodeGroup(context.Background(), 100, "group1")
			if name == "v3" || name == "v4" {
				if e != nil || g == nil || g.SeriesID != 100 || g.Parts[0].Episodes[0].ID != 1002 || g.Parts[0].Episodes[0].Order != 1 || g.Parts[0].Episodes[0].Season != 2 || g.Parts[0].Episodes[1].Order != 0 {
					t.Fatal("array position overwrote canonical/order evidence", g, e)
				}
				raw, _ := json.Marshal(g)
				if strings.Contains(string(raw), key) {
					t.Fatal("secret-bearing snapshot")
				}
			} else if name == "foreign" {
				if !errors.Is(e, ports.ErrNotFound) || details.Load() != 0 {
					t.Fatal("foreign group fetched or accepted", e, details.Load())
				}
			} else if !errors.Is(e, ports.ErrBadResponse) {
				t.Fatal("invalid group accepted", name, e)
			}
			if forwarded.Load() != 0 {
				t.Fatal("credential redirect")
			}
		})
	}
}
