package tmdb

import (
	"context"
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

const watchlistMovie = `{"id":42,"title":"电影","original_title":"Movie","release_date":"2024-02-29","overview":"故事","poster_path":"/p","backdrop_path":"/b"}`
const watchlistTV = `{"id":42,"media_type":"tv","name":"剧集","original_name":"Show","first_air_date":"2024-01-01"}`

func TestWatchlistIdentityCompleteRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, kind, item, title, original, yearKey string
		year, pages                                int
	}{
		{"movie", "movie", watchlistMovie, "电影", "Movie", "year", 2024, 1},
		{"tv", "tv", watchlistTV, "剧集", "Show", "first_air_date_year", 2024, 1},
		{"empty_zero_pages", "movie", "", "", "", "year", 2024, 0},
		{"empty_one_page", "tv", "", "", "", "first_air_date_year", 2024, 1},
		{"lower_year", "movie", `{"id":1,"title":"Old","release_date":"1800-01-01"}`, "Old", "", "year", 1800, 1},
		{"upper_year", "tv", `{"id":1,"name":"Future","first_air_date":"9999-12-31"}`, "Future", "", "first_air_date_year", 9999, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			if tc.item != "" {
				count = 1
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				q := r.URL.Query()
				if r.URL.Path != "/search/"+tc.kind || q.Get("query") != "a & 中文" || q.Get(tc.yearKey) != fmt.Sprint(tc.year) || q.Get("language") != "zh-CN" || q.Get("api_key") != "test-secret" || r.Header.Get("If-None-Match") != "" {
					t.Error("wrong identity route or parameters")
				}
				other := "year"
				if tc.yearKey == "year" {
					other = "first_air_date_year"
				}
				if q.Has(other) {
					t.Error("sent wrong year filter")
				}
				if _, err := fmt.Fprintf(w, `{"page":1,"total_pages":%d,"total_results":%d,"results":[%s]}`, tc.pages, count, tc.item); err != nil {
					t.Errorf("write fixture response: %v", err)
				}
			}))
			defer server.Close()
			client := NewClient("test-secret", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			got, err := client.SearchMediaIdentityCandidates(context.Background(), "a & 中文", tc.kind, tc.year)
			if err != nil || !got.Complete || got.Candidates == nil || len(got.Candidates) != count || calls.Load() != 1 {
				t.Fatalf("search=%#v err=%v calls=%d", got, err, calls.Load())
			}
			if count > 0 {
				m := got.Candidates[0]
				if m.MediaType != tc.kind || m.Title != tc.title || m.OriginalTitle != tc.original || m.Year != tc.year || m.TMDBID <= 0 {
					t.Fatalf("lost identity fields: %#v", m)
				}
				if tc.name == "movie" && (m.TMDBID != 42 || m.Overview != "故事" || m.PosterURL != imageBaseURL+"/p" || m.BackdropURL != imageBaseURL+"/b") {
					t.Fatalf("lost movie fields: %#v", m)
				}
			}
		})
	}
}

func TestWatchlistIdentityIncompletePaging(t *testing.T) {
	for _, tc := range []struct{ name, paging string }{
		{"missing_all", ""},
		{"missing_page", `"total_pages":1,"total_results":1,`},
		{"missing_pages", `"page":1,"total_results":1,`},
		{"missing_count", `"page":1,"total_pages":1,`},
		{"null_page", `"page":null,"total_pages":1,"total_results":1,`},
		{"null_pages", `"page":1,"total_pages":null,"total_results":1,`},
		{"null_count", `"page":1,"total_pages":1,"total_results":null,`},
		{"first_page", `"page":1,"total_pages":2,"total_results":21,`},
		{"second_page", `"page":2,"total_pages":2,"total_results":21,`},
		{"zero_page", `"page":0,"total_pages":1,"total_results":1,`},
		{"negative_page", `"page":-1,"total_pages":1,"total_results":1,`},
		{"negative_pages", `"page":1,"total_pages":-1,"total_results":1,`},
		{"negative_count", `"page":1,"total_pages":1,"total_results":-1,`},
		{"too_many_results", `"page":1,"total_pages":1,"total_results":2,`},
		{"too_few_results", `"page":1,"total_pages":1,"total_results":0,`},
		{"nonempty_zero_pages", `"page":1,"total_pages":0,"total_results":1,`},
		{"count_matches_multiple_pages", `"page":1,"total_pages":2,"total_results":1,`},
		{"partial_inconsistent", `"total_pages":0,"total_results":1,`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := fmt.Fprint(w, `{`+tc.paging+`"results":[`+watchlistMovie+`]}`); err != nil {
					t.Errorf("write fixture response: %v", err)
				}
			}))
			defer server.Close()
			client := NewClient("", WithBaseURL(server.URL))
			got, err := client.SearchMediaIdentityCandidates(context.Background(), "query", "movie", 2024)
			if err != nil || got.Complete || len(got.Candidates) != 1 {
				t.Fatalf("incomplete evidence promoted or lost: %#v %v", got, err)
			}
		})
	}
}

func TestWatchlistIdentityBadResponses(t *testing.T) {
	for _, tc := range []struct{ name, kind, body string }{
		{"missing_results", "movie", `{}`},
		{"null_results", "tv", `{"results":null}`},
		{"null_row", "movie", `{"results":[null]}`},
		{"wrong_movie_type", "movie", `{"results":[` + watchlistTV + `]}`},
		{"wrong_tv_type", "tv", `{"results":[` + strings.Replace(watchlistMovie, `"id":42`, `"id":42,"media_type":"movie"`, 1) + `]}`},
		{"person", "movie", `{"results":[{"id":1,"media_type":"person","title":"Person"}]}`},
		{"zero_id", "movie", `{"results":[{"id":0,"title":"Bad"}]}`},
		{"negative_id", "tv", `{"results":[{"id":-1,"name":"Bad"}]}`},
		{"fractional_id", "movie", `{"results":[{"id":1.5,"title":"Bad"}]}`},
		{"blank_title", "movie", `{"results":[{"id":1,"title":" \t"}]}`},
		{"missing_name", "tv", `{"results":[{"id":1,"title":"Wrong field"}]}`},
		{"invalid_movie_date", "movie", `{"results":[{"id":1,"title":"Bad","release_date":"2023-02-29"}]}`},
		{"invalid_tv_date", "tv", `{"results":[{"id":1,"name":"Bad","first_air_date":"1799-01-01"}]}`},
		{"invalid_after_valid", "movie", `{"results":[` + watchlistMovie + `,{"id":2,"title":null}]}`},
		{"paging_string", "movie", `{"page":"1","results":[` + watchlistMovie + `]}`},
		{"paging_fraction", "movie", `{"total_pages":1.5,"results":[` + watchlistMovie + `]}`},
		{"paging_object", "tv", `{"total_results":{},"results":[]}`},
		{"paging_overflow", "tv", `{"page":99999999999999999999999999,"results":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := fmt.Fprint(w, tc.body); err != nil {
					t.Errorf("write fixture response: %v", err)
				}
			}))
			defer server.Close()
			got, err := NewClient("", WithBaseURL(server.URL)).SearchMediaIdentityCandidates(context.Background(), "query", tc.kind, 2024)
			if !errors.Is(err, ports.ErrBadResponse) || got.Complete || got.Candidates != nil {
				t.Fatalf("bad response accepted: %#v %v", got, err)
			}
		})
	}
}

func TestWatchlistIdentityRejectedBeforeIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := NewClient("", WithBaseURL(server.URL))
	for _, tc := range []struct {
		name, query, kind string
		year              int
	}{
		{"empty", "", "movie", 2024}, {"blank", " \t\n", "tv", 2024},
		{"too_long", strings.Repeat("x", 513), "movie", 2024}, {"byte_limit", strings.Repeat("中", 171), "tv", 2024},
		{"missing_type", "q", "", 2024}, {"multi", "q", "multi", 2024}, {"all", "q", "all", 2024}, {"person", "q", "person", 2024},
		{"zero_year", "q", "movie", 0}, {"negative_year", "q", "movie", -1}, {"low_year", "q", "movie", 1799}, {"high_year", "q", "tv", 10000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.SearchMediaIdentityCandidates(context.Background(), tc.query, tc.kind, tc.year)
			if !errors.Is(err, ports.ErrRejected) || got.Complete || got.Candidates != nil {
				t.Fatalf("invalid input accepted: %#v %v", got, err)
			}
		})
	}
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		got, err := client.SearchMediaIdentityCandidates(ctx, "q", "tv", 2024)
		cancel()
		if !errors.Is(err, want) || got.Complete || got.Candidates != nil {
			t.Fatalf("canceled input accepted: %#v %v", got, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs made %d requests", calls.Load())
	}
}

type watchlistTransport func(*http.Request) (*http.Response, error)

func (f watchlistTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWatchlistIdentityLateCancellation(t *testing.T) {
	for _, status := range []int{200, 304, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":1,"results":[`+watchlistMovie+`]}`)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			httpClient := server.Client()
			transport := httpClient.Transport
			httpClient.Transport = watchlistTransport(func(r *http.Request) (*http.Response, error) {
				resp, err := transport.RoundTrip(r.Clone(context.WithoutCancel(r.Context())))
				cancel()
				return resp, err
			})
			client := NewClient("", WithBaseURL(server.URL), WithHTTPClient(httpClient))
			got, err := client.SearchMediaIdentityCandidates(ctx, "query", "movie", 2024)
			if !errors.Is(err, context.Canceled) || got.Complete || got.Candidates != nil || calls.Load() != 1 {
				t.Fatalf("late response accepted: %#v %v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestWatchlistIdentityQueryByteBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query().Get("query")) != 512 {
			t.Error("query changed")
		}
		if _, err := fmt.Fprint(w, `{"page":1,"total_pages":0,"total_results":0,"results":[]}`); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	defer server.Close()
	got, err := NewClient("", WithBaseURL(server.URL)).SearchMediaIdentityCandidates(context.Background(), strings.Repeat("x", 512), "movie", 2024)
	if err != nil || !got.Complete {
		t.Fatalf("512 bytes rejected: %#v %v", got, err)
	}
}
