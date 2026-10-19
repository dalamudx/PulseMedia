package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func detailFixture(t *testing.T, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return NewClient("detail-fixture-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
}

func TestDetailBrowserRoutesAndNamespaces(t *testing.T) {
	for _, auth := range []string{"v3", "v4"} {
		for _, language := range []string{"", "en-US&api_key=foreign?# 中文"} {
			t.Run(auth+"/"+language, func(t *testing.T) {
				key := "detail-fixture-key"
				if auth == "v4" {
					key = "ey" + strings.Repeat("x", 90)
				}
				wantLanguage := language
				if wantLanguage == "" {
					wantLanguage = "zh-CN"
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					q := r.URL.Query()
					if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" || q.Get("language") != wantLanguage || len(q["language"]) != 1 || r.Header.Get("If-None-Match") != "" {
						t.Error("wrong native request or unsafe language query")
					}
					if auth == "v4" {
						if q.Has("api_key") || r.Header.Get("Authorization") != "Bearer "+key || len(q) != 1 {
							t.Error("wrong v4 authentication")
						}
					} else if q.Get("api_key") != key || len(q["api_key"]) != 1 || len(q) != 2 || r.Header.Get("Authorization") != "" {
						t.Error("wrong v3 authentication or injected parameter")
					}
					switch r.URL.Path {
					case "/native/3/movie/7/credits":
						_, _ = io.WriteString(w, `{"id":7,"cast":[{"id":100,"name":"Movie actor","character":"Movie role"}]}`)
					case "/native/3/tv/7/credits":
						_, _ = io.WriteString(w, `{"id":7,"cast":[{"id":200,"name":"TV actor","character":"TV role"}]}`)
					case "/native/3/tv/7/season/0":
						_, _ = io.WriteString(w, `{"season_number":0,"episodes":[{"id":300,"episode_number":3,"season_number":0,"show_id":7}]}`)
					default:
						t.Error("wrong configured base or native detail route")
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(server.Close)
				client := NewClient(key, WithBaseURL(server.URL+"/native/3/"), WithHTTPClient(server.Client()))
				for _, kind := range []string{"movie", "tv"} {
					got, err := client.GetMediaCredits(t.Context(), kind, 7, language)
					name, actorID, role := "Movie actor", int64(100), "Movie role"
					if kind == "tv" {
						name, actorID, role = "TV actor", 200, "TV role"
					}
					if err != nil || got == nil || got.TMDBID != 7 || got.MediaType != kind || len(got.Cast) != 1 || got.Cast[0].TMDBID != actorID || got.Cast[0].Name != name || got.Cast[0].Character != role {
						t.Fatalf("lost namespace or cast facts: %#v, %v", got, err)
					}
				}
				got, err := client.GetTVSeasonDetails(t.Context(), 7, 0, language)
				if err != nil || got == nil || got.TMDBID != 7 || got.SeasonNumber != 0 || !got.Complete || len(got.Episodes) != 1 || got.Episodes[0].EpisodeNumber != 3 {
					t.Fatalf("special season lost: %#v, %v", got, err)
				}
				client.mu.RLock()
				defer client.mu.RUnlock()
				if calls.Load() != 3 || len(client.cache) != 0 || client.cacheRevision != 0 {
					t.Fatal("detail reads changed creation cache or skipped native requests")
				}
			})
		}
	}
}

func TestDetailBrowserRejectsInvalidRequestsBeforeIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	client := NewClient("detail-fixture-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	for _, tc := range []struct {
		kind string
		id   int64
	}{{"", 7}, {"multi", 7}, {"all", 7}, {"person", 7}, {"Movie", 7}, {"../movie", 7}, {"movie", 0}, {"tv", -1}} {
		t.Run(fmt.Sprintf("credits/%s/%d", tc.kind, tc.id), func(t *testing.T) {
			if got, err := client.GetMediaCredits(t.Context(), tc.kind, tc.id, ""); got != nil || !errors.Is(err, ports.ErrRejected) {
				t.Fatalf("invalid credits request accepted: %#v, %v", got, err)
			}
		})
	}
	for _, tc := range []struct {
		id     int64
		season int
	}{{0, 1}, {-1, 0}, {7, -1}} {
		t.Run(fmt.Sprintf("season/%d/%d", tc.id, tc.season), func(t *testing.T) {
			if got, err := client.GetTVSeasonDetails(t.Context(), tc.id, tc.season, ""); got != nil || !errors.Is(err, ports.ErrRejected) {
				t.Fatalf("invalid season request accepted: %#v, %v", got, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request performed I/O")
	}
}

func TestDetailBrowserCreditsEnvelopeAndCap(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"null", `null`}, {"array", `[]`}, {"broken", `{"id":`},
		{"missing-id", `{"cast":[]}`}, {"foreign-id", `{"id":8,"cast":[]}`},
		{"missing-cast", `{"id":7}`}, {"null-cast", `{"id":7,"cast":null}`}, {"object-cast", `{"id":7,"cast":{}}`},
		{"null-actor", `{"id":7,"cast":[null]}`}, {"missing-actor-id", `{"id":7,"cast":[{"name":"Actor"}]}`},
		{"negative-actor-id", `{"id":7,"cast":[{"id":-1,"name":"Actor"}]}`}, {"empty-name", `{"id":7,"cast":[{"id":1,"name":" \t"}]}`},
		{"bad-name-type", `{"id":7,"cast":[{"id":1,"name":42}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detailFixture(t, tc.body).GetMediaCredits(t.Context(), "movie", 7, "")
			if got != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("malformed credits became usable: %#v, %v", got, err)
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		got, err := detailFixture(t, `{"id":7,"cast":[]}`).GetMediaCredits(t.Context(), "tv", 7, "")
		if err != nil || got == nil || got.Cast == nil || len(got.Cast) != 0 || got.TMDBID != 7 || got.MediaType != "tv" {
			t.Fatalf("empty cast not preserved: %#v, %v", got, err)
		}
	})
	for _, invalidTail := range []bool{false, true} {
		t.Run(fmt.Sprintf("cap/invalid-tail-%t", invalidTail), func(t *testing.T) {
			cast := make([]map[string]any, 27)
			for i := range cast {
				cast[i] = map[string]any{"id": 100 - i, "name": fmt.Sprintf("Actor %d", i), "character": fmt.Sprintf("Role %d", i), "order": 27 - i, "profile_path": "/actor.jpg"}
			}
			if invalidTail {
				cast[26]["id"] = 0
			}
			body, err := json.Marshal(map[string]any{"id": 7, "cast": cast})
			if err != nil {
				t.Fatal(err)
			}
			got, err := detailFixture(t, string(body)).GetMediaCredits(t.Context(), "movie", 7, "")
			if invalidTail {
				if got != nil || !errors.Is(err, ports.ErrBadResponse) {
					t.Fatal("invalid cast tail hidden by cap", err)
				}
				return
			}
			if err != nil || got == nil || len(got.Cast) != 24 {
				t.Fatalf("wrong cast cap: %#v, %v", got, err)
			}
			for i, actor := range got.Cast {
				if actor.TMDBID != int64(100-i) || actor.Name != fmt.Sprintf("Actor %d", i) || actor.Character != fmt.Sprintf("Role %d", i) || actor.ProfileURL != imageBaseURL+"/actor.jpg" {
					t.Fatal("cast reordered or provider facts lost", i, actor)
				}
			}
		})
	}
}

func TestDetailBrowserSparseSeasonsAndUnknowns(t *testing.T) {
	for _, order := range []string{"1,3", "3,1"} {
		t.Run(order, func(t *testing.T) {
			episodes := []map[string]any{
				{"id": 101, "episode_number": 1, "season_number": 1, "show_id": 7, "name": "First", "overview": "Plot", "air_date": "2024-02-29", "still_path": "/still_1-abc.jpg"},
				{"id": 103, "episode_number": 3, "air_date": nil, "still_path": nil},
			}
			want := []ports.MetadataEpisodeDetails{
				{TMDBID: 101, EpisodeNumber: 1, Name: "First", Overview: "Plot", AirDate: "2024-02-29", StillURL: imageBaseURL + "/still_1-abc.jpg"},
				{TMDBID: 103, EpisodeNumber: 3},
			}
			if order == "3,1" {
				episodes[0], episodes[1] = episodes[1], episodes[0]
				want[0], want[1] = want[1], want[0]
			}
			body, err := json.Marshal(map[string]any{"season_number": 1, "episode_count": 99, "episodes": episodes})
			if err != nil {
				t.Fatal(err)
			}
			got, err := detailFixture(t, string(body)).GetTVSeasonDetails(t.Context(), 7, 1, "")
			if err != nil || got == nil || got.TMDBID != 7 || got.SeasonNumber != 1 || !got.Complete || !reflect.DeepEqual(got.Episodes, want) {
				t.Fatalf("sparse facts changed: %#v, %v", got, err)
			}
		})
	}
	for _, fields := range []string{"", `,"air_date":""`, `,"air_date":null,"name":null,"overview":null`} {
		t.Run("unknown"+fields, func(t *testing.T) {
			body := `{"season_number":0,"episodes":[{"id":101,"episode_number":1` + fields + `}]}`
			got, err := detailFixture(t, body).GetTVSeasonDetails(t.Context(), 7, 0, "")
			if err != nil || got == nil || !got.Complete || len(got.Episodes) != 1 || got.Episodes[0] != (ports.MetadataEpisodeDetails{TMDBID: 101, EpisodeNumber: 1}) {
				t.Fatalf("unknown values fabricated: %#v, %v", got, err)
			}
		})
	}
	for _, season := range []int{0, 1, 1000} {
		t.Run(fmt.Sprintf("empty/%d", season), func(t *testing.T) {
			got, err := detailFixture(t, fmt.Sprintf(`{"season_number":%d,"episode_count":99,"episodes":[]}`, season)).GetTVSeasonDetails(t.Context(), 7, season, "")
			if err != nil || got == nil || !got.Complete || got.TMDBID != 7 || got.SeasonNumber != season || got.Episodes == nil || len(got.Episodes) != 0 {
				t.Fatalf("empty season fabricated or rejected: %#v, %v", got, err)
			}
		})
	}
}

func TestDetailBrowserRejectsIncompleteOrForeignSeasons(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"null", `null`}, {"array", `[]`}, {"broken", `{"episodes":`},
		{"missing-season-zero", `{"episodes":[]}`}, {"null-season-zero", `{"season_number":null,"episodes":[]}`},
		{"foreign-season", `{"season_number":2,"episodes":[]}`}, {"missing-episodes", `{"season_number":0}`},
		{"null-episodes", `{"season_number":0,"episodes":null}`}, {"object-episodes", `{"season_number":0,"episodes":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detailFixture(t, tc.body).GetTVSeasonDetails(t.Context(), 7, 0, "")
			if got != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("incomplete season became usable: %#v, %v", got, err)
			}
		})
	}
	for _, tc := range []struct{ name, episodes string }{
		{"null-episode", `null`}, {"missing-id", `{"episode_number":1}`},
		{"negative-id", `{"id":-1,"episode_number":1}`}, {"missing-number", `{"id":101}`},
		{"zero-number", `{"id":101,"episode_number":0}`}, {"negative-number", `{"id":101,"episode_number":-1}`},
		{"foreign-season", `{"id":101,"episode_number":1,"season_number":2}`},
		{"null-season", `{"id":101,"episode_number":1,"season_number":null}`},
		{"foreign-show", `{"id":101,"episode_number":1,"show_id":8}`},
		{"null-show", `{"id":101,"episode_number":1,"show_id":null}`},
		{"wrong-show-type", `{"id":101,"episode_number":1,"show_id":"7"}`},
		{"duplicate-number", `{"id":101,"episode_number":1},{"id":103,"episode_number":1}`},
		{"duplicate-id", `{"id":101,"episode_number":1},{"id":101,"episode_number":3}`},
		{"invalid-date", `{"id":101,"episode_number":1,"air_date":"2026-02-29"}`},
		{"loose-date", `{"id":101,"episode_number":1,"air_date":"2026-1-01"}`},
		{"timestamp-date", `{"id":101,"episode_number":1,"air_date":"2026-01-01T00:00:00Z"}`},
		{"spaced-date", `{"id":101,"episode_number":1,"air_date":" 2026-01-01"}`},
		{"wrong-date-type", `{"id":101,"episode_number":1,"air_date":20260101}`},
		{"bad-tail", `{"id":101,"episode_number":1},{"id":103,"episode_number":3,"show_id":8}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detailFixture(t, `{"season_number":1,"episodes":[`+tc.episodes+`]}`).GetTVSeasonDetails(t.Context(), 7, 1, "")
			if got != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("invalid episode granted complete result: %#v, %v", got, err)
			}
		})
	}
	t.Run("episode-bound", func(t *testing.T) {
		var episodes strings.Builder
		for i := 1; i <= 5001; i++ {
			if i > 1 {
				episodes.WriteByte(',')
			}
			_, _ = fmt.Fprintf(&episodes, `{"id":%d,"episode_number":%d}`, i, i)
		}
		got, err := detailFixture(t, `{"season_number":1,"episodes":[`+episodes.String()+`]}`).GetTVSeasonDetails(t.Context(), 7, 1, "")
		if got != nil || !errors.Is(err, ports.ErrBadResponse) {
			t.Fatal("unbounded provider season accepted", err)
		}
	})
}

func TestDetailBrowserImageProjection(t *testing.T) {
	for _, path := range []string{"/safe_Ab9-x.jpg", "", "/", "/.", "/..", "/../secret", "//evil.example/x.jpg", "https://evil.example/x.jpg", "/https://evil.example/x.jpg", "/nested/x.jpg", "/%2e%2e%2fsecret", "/x.jpg?token=secret", "/x.jpg#secret", "/x\\secret", "/x\n.jpg", "/图片.jpg"} {
		t.Run(path, func(t *testing.T) {
			encoded, err := json.Marshal(path)
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if path == "/safe_Ab9-x.jpg" {
				want = imageBaseURL + path
			}
			credits, err := detailFixture(t, `{"id":7,"cast":[{"id":101,"name":"Actor","profile_path":`+string(encoded)+`} ]}`).GetMediaCredits(t.Context(), "movie", 7, "")
			if err != nil || credits == nil || len(credits.Cast) != 1 || credits.Cast[0].ProfileURL != want {
				t.Fatalf("unsafe profile or lost actor: %#v, %v", credits, err)
			}
			season, err := detailFixture(t, `{"season_number":1,"episodes":[{"id":101,"episode_number":1,"still_path":`+string(encoded)+`}]}`).GetTVSeasonDetails(t.Context(), 7, 1, "")
			if err != nil || season == nil || !season.Complete || len(season.Episodes) != 1 || season.Episodes[0].StillURL != want {
				t.Fatalf("unsafe still or lost episode: %#v, %v", season, err)
			}
		})
	}
}

// Both capabilities must retain the transport's safe error and cancellation contracts.
func TestDetailBrowserTransportFailures(t *testing.T) {
	readers := []struct {
		name string
		read func(context.Context, *Client) (bool, error)
	}{
		{"credits", func(ctx context.Context, c *Client) (bool, error) {
			v, e := c.GetMediaCredits(ctx, "tv", 7, "")
			return v == nil, e
		}},
		{"season", func(ctx context.Context, c *Client) (bool, error) {
			v, e := c.GetTVSeasonDetails(ctx, 7, 1, "")
			return v == nil, e
		}},
	}
	for _, reader := range readers {
		t.Run(reader.name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				status int
				want   error
			}{
				{"not-found", 404, ports.ErrNotFound}, {"rate", 429, nil}, {"unauthorized", 401, ports.ErrAuthRequired},
				{"forbidden", 403, ports.ErrAuthRequired}, {"unavailable", 503, ports.ErrUnavailable},
				{"not-modified", 304, ports.ErrBadResponse}, {"redirect", 302, ports.ErrBadResponse}, {"oversized", 200, ports.ErrBadResponse},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						w.Header().Set("Retry-After", "12")
						w.Header().Set("Location", "/trap?secret=provider-secret")
						w.WriteHeader(tc.status)
						body := "provider-secret"
						if tc.name == "oversized" {
							body = strings.Repeat(" ", 5<<20) + `{}`
						}
						_, _ = io.WriteString(w, body)
					}))
					t.Cleanup(server.Close)
					client := NewClient("detail-fixture-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
					isNil, err := reader.read(t.Context(), client)
					if !isNil || err == nil || strings.Contains(err.Error(), "provider-secret") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "detail-fixture-key") {
						t.Fatalf("unsafe error/result: nil=%t, %v", isNil, err)
					}
					if tc.status == 429 {
						if rate, ok := ports.IsRateLimited(err); !ok || rate.RetryAfter != 12*time.Second {
							t.Fatal("lost retry advice", err)
						}
						if _, next := reader.read(t.Context(), client); next == nil {
							t.Fatal("shared cooldown ignored")
						}
					} else if !errors.Is(err, tc.want) {
						t.Fatal("wrong safe provider error", err)
					}
					if calls.Load() != 1 {
						t.Fatal("followed redirect or ignored budget cooldown")
					}
				})
			}
			for _, phase := range []string{"before", "in-flight"} {
				t.Run("cancel/"+phase, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						cancel()
						<-r.Context().Done()
					}))
					t.Cleanup(server.Close)
					if phase == "before" {
						cancel()
					}
					client := NewClient("detail-fixture-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
					isNil, err := reader.read(ctx, client)
					wantCalls := int32(0)
					if phase == "in-flight" {
						wantCalls = 1
					}
					if !isNil || !errors.Is(err, context.Canceled) || calls.Load() != wantCalls {
						t.Fatal("cancellation contract lost", isNil, err, calls.Load())
					}
				})
			}
			t.Run("raw-transport-error", func(t *testing.T) {
				transport := detailTransport(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("https://provider.invalid?api_key=secret")
				})
				client := NewClient("detail-fixture-key", WithHTTPClient(&http.Client{Transport: transport}))
				isNil, err := reader.read(t.Context(), client)
				if !isNil || err != ports.ErrUnavailable {
					t.Fatal("raw transport error leaked", isNil, err)
				}
			})
		})
	}
}

type detailTransport func(*http.Request) (*http.Response, error)

func (f detailTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
