package tmdb

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const tvdbFindFixture = `{"tv_episode_results":[{"id":701,"show_id":1396,"season_number":1,"episode_number":7}]}`
const tvdbEpisodeFixture = `{"id":701,"show_id":1396,"season_number":1,"episode_number":7,"air_date":"2008-03-09"}`

func tvdbEpisodeNative(t *testing.T, key, findBody, episodeBody string) (*Client, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.Header.Get("If-None-Match") != "" {
			t.Error("wrong read method or conditional request")
		}
		if strings.HasPrefix(key, "ey") {
			if r.Header.Get("Authorization") != "Bearer "+key || r.URL.Query().Has("api_key") {
				t.Error("incorrect v4 authentication")
			}
		} else if r.URL.Query().Get("api_key") != key {
			t.Error("incorrect v3 authentication")
		}
		switch r.URL.Path {
		case "/find/9002":
			if r.URL.Query().Get("external_source") != "tvdb_id" {
				t.Error("wrong external namespace")
			}
			_, _ = io.WriteString(w, findBody)
		case "/tv/1396/season/1/episode/7":
			_, _ = io.WriteString(w, episodeBody)
		default:
			t.Error("unproven canonical coordinate requested")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return NewClient(key, WithBaseURL(server.URL), WithHTTPClient(server.Client())), calls
}
func TestTVDBEpisodeCanonicalResolution(t *testing.T) {
	for _, key := range []string{"owned-key", "ey" + strings.Repeat("a", 70)} {
		t.Run(fmt.Sprint(len(key)), func(t *testing.T) {
			c, calls := tvdbEpisodeNative(t, key, tvdbFindFixture, tvdbEpisodeFixture)
			got, err := c.ResolveTVDBEpisode(t.Context(), 1396, 9002)
			if err != nil || got == nil || got.TVDBEpisodeID != 9002 || got.TMDBSeriesID != 1396 || got.SeasonNumber != 1 || got.Episode.TMDBEpisodeID != 701 || got.Episode.EpisodeNumber != 7 || got.Episode.AirDate == nil || got.Episode.AirDate.Format("2006-01-02") != "2008-03-09" || got.Episode.AbsoluteNumber != nil || calls.Load() != 2 {
				t.Fatalf("unproven canonical episode: %+v %v", got, err)
			}
		})
	}
}
func TestTVDBEpisodeFindRejectsUnprovenIdentity(t *testing.T) {
	for name, body := range map[string]string{
		"missing_results":   `{}`,
		"null_results":      `{"tv_episode_results":null}`,
		"foreign_series":    strings.Replace(tvdbFindFixture, `"show_id":1396`, `"show_id":1397`, 1),
		"missing_series":    strings.Replace(tvdbFindFixture, `"show_id":1396,`, "", 1),
		"duplicate_id":      strings.Replace(tvdbFindFixture, `"id":701`, `"id":701,"ID":702`, 1),
		"ambiguous_results": `{"tv_episode_results":[{"id":701,"show_id":1396,"season_number":1,"episode_number":7},{"id":702,"show_id":1396,"season_number":1,"episode_number":7}]}`,
		"fractional_id":     strings.Replace(tvdbFindFixture, `"id":701`, `"id":701.0`, 1),
		"null_season":       strings.Replace(tvdbFindFixture, `"season_number":1`, `"season_number":null`, 1),
		"missing_season":    strings.Replace(tvdbFindFixture, `"season_number":1,`, "", 1),
		"zero_episode":      strings.Replace(tvdbFindFixture, `"episode_number":7`, `"episode_number":0`, 1),
		"duplicate_results": `{"tv_episode_results":[],"TV_EPISODE_RESULTS":[]}`,
		"trailing":          tvdbFindFixture + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, calls := tvdbEpisodeNative(t, "owned-key", body, tvdbEpisodeFixture)
			got, err := c.ResolveTVDBEpisode(t.Context(), 1396, 9002)
			if got != nil || !errors.Is(err, ports.ErrBadResponse) || calls.Load() != 1 {
				t.Fatalf("unproven mapping accepted: %v calls=%d", err, calls.Load())
			}
		})
	}
	c, calls := tvdbEpisodeNative(t, "owned-key", `{"tv_episode_results":[]}`, tvdbEpisodeFixture)
	if got, err := c.ResolveTVDBEpisode(t.Context(), 1396, 9002); got != nil || !errors.Is(err, ports.ErrNotFound) || calls.Load() != 1 {
		t.Fatalf("explicit no-match not preserved: %v", err)
	}
}
func TestTVDBEpisodeHydrationRejectsChangedFacts(t *testing.T) {
	for name, body := range map[string]string{
		"changed_identity":     strings.Replace(tvdbEpisodeFixture, `"id":701`, `"id":702`, 1),
		"changed_series":       strings.Replace(tvdbEpisodeFixture, `"show_id":1396`, `"show_id":1397`, 1),
		"changed_season":       strings.Replace(tvdbEpisodeFixture, `"season_number":1`, `"season_number":2`, 1),
		"changed_episode":      strings.Replace(tvdbEpisodeFixture, `"episode_number":7`, `"episode_number":2`, 1),
		"duplicate_coordinate": strings.Replace(tvdbEpisodeFixture, `"episode_number":7`, `"episode_number":7,"episode_number":2`, 1),
		"invalid_date":         strings.Replace(tvdbEpisodeFixture, `2008-03-09`, `2008-02-30`, 1),
		"unknown_date_field":   `{"id":701,"season_number":1,"episode_number":7}`,
		"trailing":             tvdbEpisodeFixture + `null`,
	} {
		t.Run(name, func(t *testing.T) {
			c, calls := tvdbEpisodeNative(t, "owned-key", tvdbFindFixture, body)
			got, err := c.ResolveTVDBEpisode(t.Context(), 1396, 9002)
			if got != nil || !errors.Is(err, ports.ErrBadResponse) || calls.Load() != 2 {
				t.Fatalf("changed canonical facts accepted: %v", err)
			}
		})
	}
	for _, date := range []string{`null`, `""`} {
		c, _ := tvdbEpisodeNative(t, "owned-key", tvdbFindFixture, strings.Replace(tvdbEpisodeFixture, `"2008-03-09"`, date, 1))
		got, err := c.ResolveTVDBEpisode(t.Context(), 1396, 9002)
		if err != nil || got.Episode.AirDate != nil || got.Episode.AbsoluteNumber != nil {
			t.Fatalf("unknown date fabricated: %+v %v", got, err)
		}
	}
}
func TestTVDBEpisodeResolutionInputAndHTTPFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "https://foreign.example/secret")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	c := NewClient("owned-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	for _, ids := range [][2]int64{{0, 9002}, {1396, 0}, {-1, 9002}} {
		if _, err := c.ResolveTVDBEpisode(t.Context(), ids[0], ids[1]); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid identity accepted", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid identity accessed upstream")
	}
	if got, err := c.ResolveTVDBEpisode(t.Context(), 1396, 9002); got != nil || err == nil || strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
		t.Fatalf("redirect followed or exposed: %v", err)
	}
}
