package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestCatalogSearchRoutes(t *testing.T) {
	for _, tc := range []struct {
		kind, endpoint, yearKey, body string
		year, want                    int
	}{
		{"movie", "movie", "year", `{"results":[{"id":1,"title":"电影","original_title":"Movie","release_date":"2024-02-29","overview":"故事","poster_path":"/p","backdrop_path":"/b"}]}`, 2024, 1},
		{"tv", "tv", "first_air_date_year", `{"results":[{"id":2,"name":"剧集","original_name":"Show","first_air_date":"2024-01-01"}]}`, 2024, 1},
		{"multi", "multi", "", `{"results":[{"media_type":"person","id":"ignored"},{"media_type":"movie","id":1,"title":"Movie","release_date":"2023-01-01"},{"media_type":"tv","id":2,"name":"Show","first_air_date":"2024-01-01"},{"media_type":"tv","id":3,"name":"Unknown","first_air_date":null}]}`, 2024, 1},
		{"all", "multi", "", `{"results":[]}`, 0, 0},
		{"", "multi", "", `{"results":[{"media_type":"movie","id":1,"title":"Undated"}]}`, 0, 1},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				q := r.URL.Query()
				if r.URL.Path != "/search/"+tc.endpoint || q.Get("query") != "a & 中文" || q.Get("language") != "zh-CN" || q.Get("api_key") != "test-secret" {
					t.Errorf("unexpected route or parameters")
				}
				for _, key := range []string{"year", "first_air_date_year"} {
					expected := ""
					if key == tc.yearKey {
						expected = fmt.Sprint(tc.year)
					}
					if q.Get(key) != expected {
						t.Errorf("wrong %s", key)
					}
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewClient("test-secret", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			got, err := client.searchCatalogMedia(context.Background(), "a & 中文", tc.kind, tc.year)
			if err != nil || got == nil || len(got) != tc.want || calls.Load() != 1 {
				t.Fatalf("got %#v, %v; calls %d", got, err, calls.Load())
			}
			if tc.kind == "movie" && (got[0].Title != "电影" || got[0].OriginalTitle != "Movie" || got[0].Overview != "故事" || got[0].Year != 2024 || got[0].PosterURL != imageBaseURL+"/p" || got[0].BackdropURL != imageBaseURL+"/b" || got[0].MediaType != "movie") {
				t.Fatalf("lost browse fields: %#v", got[0])
			}
			if tc.kind == "tv" && (got[0].Title != "剧集" || got[0].OriginalTitle != "Show" || got[0].Year != 2024 || got[0].MediaType != "tv") {
				t.Fatalf("lost TV fields: %#v", got[0])
			}
		})
	}
}

func TestCatalogSearchEmptyAndRejectedDoNotRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); t.Error("unexpected request") }))
	defer server.Close()
	client := NewClient("", WithBaseURL(server.URL))
	for _, query := range []string{"", " \t\n"} {
		got, err := client.searchCatalogMedia(context.Background(), query, "multi", 0)
		raw, _ := json.Marshal(got)
		if err != nil || string(raw) != "[]" {
			t.Fatalf("empty query: %s %v", raw, err)
		}
	}
	for _, tc := range []struct {
		kind string
		year int
	}{{"person", 0}, {"movie", -1}, {"tv", 10000}} {
		if _, err := client.searchCatalogMedia(context.Background(), "query", tc.kind, tc.year); !errors.Is(err, ports.ErrRejected) {
			t.Fatalf("expected rejected: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("request made")
	}
}

func TestCatalogSearchRejectsMalformedResults(t *testing.T) {
	cases := []struct{ kind, body string }{
		{"multi", `{}`}, {"multi", `null`}, {"multi", `{"results":null}`}, {"multi", `{"results":{}}`}, {"multi", `{"results":[null]}`},
		{"multi", `{"results":[{"id":1,"title":"No type"}]}`},
		{"multi", `{"results":[{"media_type":"unknown","id":1}]}`},
		{"movie", `{"results":[{"id":0,"title":"Bad"}]}`},
		{"movie", `{"results":[{"id":-1,"title":"Bad"}]}`},
		{"movie", `{"results":[{"id":1.5,"title":"Bad"}]}`},
		{"movie", `{"results":[{"id":1,"title":"  "}]}`},
		{"movie", `{"results":[{"id":1,"title":"Bad","media_type":"tv"}]}`},
		{"tv", `{"results":[{"id":1,"name":"Bad","media_type":"movie"}]}`},
		{"movie", `{"results":[{"id":1,"title":"Bad","release_date":"2023-02-29"}]}`},
		{"tv", `{"results":[{"id":1,"name":"Bad","first_air_date":"0000-01-01"}]}`},
		{"tv", `{"results":[{"id":1,"name":"Bad","first_air_date":"2024"}]}`},
		{"multi", `{"results":[{"id":1,"media_type":"movie","title":"Bad","release_date":"1900-99-99"}]}`},
		{"movie", `{"results":[{"id":1,"title":"Good"},{"id":2,"title":null}]}`},
		{"movie", `{"results":[{"id":1,"title":"secret-value","poster_path":{}}]}`},
		{"movie", `{"results":`},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			client := NewClient("secret-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			got, err := client.searchCatalogMedia(context.Background(), "secret-query", tc.kind, 2024)
			if !errors.Is(err, ports.ErrBadResponse) || got != nil {
				t.Fatalf("expected bad response: %#v %v", got, err)
			}
			for _, secret := range []string{"secret-key", "secret-query", "secret-value", server.URL} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("error leaked input")
				}
			}
		})
	}
}

func TestCatalogSearchTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{401, ports.ErrAuthRequired}, {403, ports.ErrAuthRequired}, {429, nil}, {500, ports.ErrUnavailable}, {304, ports.ErrBadResponse}, {302, ports.ErrBadResponse}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer ey-test-token" || r.URL.Query().Has("api_key") {
					t.Error("wrong bearer authentication")
				}
				w.Header().Set("Retry-After", "1")
				w.Header().Set("Location", "/should-not-follow")
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client := NewClient("ey-test-token", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			_, err := client.searchCatalogMedia(context.Background(), "query", "multi", 0)
			if tc.status == 429 {
				rate, ok := ports.IsRateLimited(err)
				if !ok || rate.RetryAfter != time.Second {
					t.Fatalf("got %v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("cancel", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled call sent request") }))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := NewClient("", WithBaseURL(server.URL))
		if _, err := client.searchCatalogMedia(ctx, "query", "tv", 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		httpClient := server.Client()
		httpClient.Timeout = 50 * time.Millisecond
		client := NewClient("", WithBaseURL(server.URL), WithHTTPClient(httpClient))
		if _, err := client.searchCatalogMedia(context.Background(), "query", "movie", 0); !errors.Is(err, ports.ErrTimeout) {
			t.Fatalf("got %v", err)
		}
	})
}

const catalogTVFixture = `{"id":1399,"name":"权力的游戏","original_name":"Game of Thrones","first_air_date":"2011-04-17","overview":"故事","vote_average":8.5,"poster_path":"/p","backdrop_path":"/b","genres":[{"name":"剧情"}],"seasons":[{"season_number":0,"episode_count":2,"name":"","air_date":null},{"season_number":1,"episode_count":10,"air_date":"2011-04-17","poster_path":"/s"},{"season_number":3,"episode_count":0,"air_date":""}],"number_of_seasons":2,"number_of_episodes":10}`

func TestCatalogTVDetailsFieldsAndCounts(t *testing.T) {
	got, err := decodeCatalogTVDetails([]byte(catalogTVFixture), 1399)
	if err != nil {
		t.Fatal(err)
	}
	if got.TMDBID != 1399 || got.Title != "权力的游戏" || got.OriginalTitle != "Game of Thrones" || got.Year != 2011 || got.Overview != "故事" || got.Rating != 8.5 || got.PosterURL != imageBaseURL+"/p" || got.BackdropURL != imageBaseURL+"/b" || len(got.Genres) != 1 || got.Genres[0] != "剧情" || got.SeasonsCount != 2 || got.EpisodesCount != 10 {
		t.Fatalf("lost TV fields: %#v", got)
	}
	if len(got.Seasons) != 3 || got.Seasons[0].Name != "特别篇" || got.Seasons[1].Name != "第 1 季" || got.Seasons[1].PosterURL != imageBaseURL+"/s" || got.Seasons[1].AirDate != "2011-04-17" || got.Seasons[2].SeasonNumber != 3 {
		t.Fatalf("lost season fields: %#v", got.Seasons)
	}
	withoutSummaries := strings.ReplaceAll(strings.ReplaceAll(catalogTVFixture, `,"number_of_seasons":2`, ""), `,"number_of_episodes":10`, "")
	derived, err := decodeCatalogTVDetails([]byte(withoutSummaries), 1399)
	if err != nil || derived.SeasonsCount != 2 || derived.EpisodesCount != 10 {
		t.Fatalf("derived totals: %#v %v", derived, err)
	}
	empty, err := decodeCatalogTVDetails([]byte(`{"id":1,"name":"Upcoming","first_air_date":null,"seasons":[]}`), 1)
	if err != nil || empty.Seasons == nil || empty.Year != 0 || empty.SeasonsCount != 0 || empty.EpisodesCount != 0 {
		t.Fatalf("empty seasons: %#v %v", empty, err)
	}
}

func TestCatalogTVDetailsRejectsInvalidPayload(t *testing.T) {
	for _, tc := range []struct{ old, replacement string }{
		{`"id":1399`, `"id":0`}, {`"id":1399`, `"id":1400`},
		{`"name":"权力的游戏"`, `"name":" "`},
		{`"first_air_date":"2011-04-17"`, `"first_air_date":"2011-02-29"`},
		{`"season_number":3`, `"season_number":1`},
		{`"season_number":3`, `"season_number":-1`},
		{`"season_number":3`, `"season_number":null`},
		{`"season_number":3`, `"other":3`},
		{`"episode_count":10`, `"episode_count":-1`},
		{`"episode_count":10`, `"episode_count":null`},
		{`"episode_count":10`, `"other":10`},
		{`"air_date":"2011-04-17"`, `"air_date":"invalid"`},
		{`"number_of_seasons":2`, `"number_of_seasons":3`},
		{`"number_of_seasons":2`, `"number_of_seasons":null`},
		{`"number_of_seasons":2`, `"number_of_seasons":-1`},
		{`"number_of_episodes":10`, `"number_of_episodes":12`},
		{`"number_of_episodes":10`, `"number_of_episodes":null`},
		{`"number_of_episodes":10`, `"number_of_episodes":1.5`},
		{`"id":1399`, `"id":1399,"media_type":"movie"`},
	} {
		t.Run(tc.old+tc.replacement, func(t *testing.T) {
			raw := strings.Replace(catalogTVFixture, tc.old, tc.replacement, 1)
			if got, err := decodeCatalogTVDetails([]byte(raw), 1399); !errors.Is(err, ports.ErrBadResponse) || got != nil {
				t.Fatalf("got %#v %v", got, err)
			}
		})
	}
	for _, raw := range []string{`null`, `{}`, `{"id":1399,"name":"secret-name"}`, `{"id":1399,"name":"secret-name","seasons":null}`, `{"id":1399,"name":"secret-name","seasons":[null]}`, `{"id":1399,"name":"secret-name","seasons":{}}`, `{"id":1399,"name":"secret-name","seasons":[{"season_number":1,"episode_count":9223372036854775807},{"season_number":2,"episode_count":1}]}`} {
		got, err := decodeCatalogTVDetails([]byte(raw), 1399)
		if !errors.Is(err, ports.ErrBadResponse) || got != nil || strings.Contains(err.Error(), "secret-name") {
			t.Fatalf("got %#v %v", got, err)
		}
	}
	if _, err := decodeCatalogTVDetails([]byte(catalogTVFixture), 0); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("invalid requested ID: %v", err)
	}
}
