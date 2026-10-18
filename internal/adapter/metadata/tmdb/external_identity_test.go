package tmdb_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestFindMovieRequiresUnambiguousExternalIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, response, imdb string
		want                 error
		details              int
	}{
		{"two_movies", `{"movie_results":[{"id":550},{"id":999}]}`, "tt0137523", ports.ErrBadResponse, 0},
		{"duplicate_rows", `{"movie_results":[{"id":550},{"id":550}]}`, "tt0137523", ports.ErrBadResponse, 0},
		{"zero_id", `{"movie_results":[{"id":0}]}`, "tt0137523", ports.ErrBadResponse, 0},
		{"negative_id", `{"movie_results":[{"id":-1}]}`, "tt0137523", ports.ErrBadResponse, 0},
		{"missing_id", `{"movie_results":[{}]}`, "tt0137523", ports.ErrBadResponse, 0},
		{"missing_results", `{}`, "tt0137523", ports.ErrBadResponse, 0},
		{"null_results", `{"movie_results":null}`, "tt0137523", ports.ErrBadResponse, 0},
		{"no_movie", `{"movie_results":[],"tv_results":[{"id":550}]}`, "tt0137523", ports.ErrNotFound, 0},
		{"wrong_imdb", `{"movie_results":[{"id":550}]}`, "tt0000001", ports.ErrBadResponse, 1},
		{"missing_imdb", `{"movie_results":[{"id":550}]}`, "", ports.ErrBadResponse, 1},
		{"one_movie", `{"movie_results":[{"id":550}]}`, "tt0137523", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var finds, details, other atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/find/tt0137523":
					finds.Add(1)
					if r.URL.Query().Get("external_source") != "imdb_id" || r.URL.Query().Get("language") != "zh-CN" {
						t.Errorf("unexpected Find query: %s", r.URL.RawQuery)
					}
					_, _ = fmt.Fprint(w, tc.response)
				case "/movie/550", "/movie/0", "/movie/-1":
					details.Add(1)
					_, _ = fmt.Fprintf(w, `{"id":550,"imdb_id":%q,"title":"Fight Club","release_date":"1999-10-15","overview":"Evidence"}`, tc.imdb)
				default:
					other.Add(1)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL))
			got, err := client.FindMovieByExternalID(context.Background(), "imdb", "tt0137523")
			if tc.want != nil {
				if !errors.Is(err, tc.want) || got != nil {
					t.Fatalf("uncertain external identity accepted: %#v err=%v want=%v", got, err, tc.want)
				}
			} else if err != nil || got == nil || got.TMDBID != 550 || got.IMDbID != "tt0137523" {
				t.Fatalf("unique identity not resolved: %#v err=%v", got, err)
			}
			if finds.Load() != 1 || int(details.Load()) != tc.details || other.Load() != 0 {
				t.Fatalf("unexpected fallback or details: find=%d details=%d other=%d", finds.Load(), details.Load(), other.Load())
			}
		})
	}
}

func TestFindMovieRejectsInvalidExternalRequestWithoutNetwork(t *testing.T) {
	for _, tc := range []struct{ namespace, id string }{
		{"imdb", ""}, {"imdb", "tt0000000"}, {"imdb", "../tt0137523"}, {"imdb", "tt0137523?credential=fixture"}, {"imdb", "tt123"}, {"imdb", "tt0137523extra"},
		{"tvdb", "0"}, {"tvdb", "+123"}, {"tvdb", "00123"}, {"tvdb", "-1"}, {"tvdb", "9223372036854775808"}, {"tvdb", "../123"}, {"unknown", "fixture"},
	} {
		t.Run(tc.namespace+"_"+tc.id, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = fmt.Fprint(w, `{"movie_results":[]}`)
			}))
			defer server.Close()
			got, err := tmdb.NewClient("", tmdb.WithBaseURL(server.URL)).FindMovieByExternalID(context.Background(), tc.namespace, tc.id)
			if !errors.Is(err, ports.ErrRejected) || got != nil || calls.Load() != 0 {
				t.Fatalf("invalid lookup admitted: %#v err=%v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestFindMovieNativeNamespaceAndErrors(t *testing.T) {
	for _, tc := range []struct{ namespace, id, path, source string }{
		{"IMDB", "TT0137523", "/find/tt0137523", "imdb_id"},
		{"tvdb", "123", "/find/123", "tvdb_id"},
	} {
		t.Run(tc.namespace, func(t *testing.T) {
			var finds, details atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case tc.path:
					finds.Add(1)
					if r.URL.Query().Get("external_source") != tc.source {
						t.Errorf("wrong external source: %s", r.URL.RawQuery)
					}
					_, _ = fmt.Fprint(w, `{"movie_results":[{"id":550}]}`)
				case "/movie/550":
					details.Add(1)
					_, _ = fmt.Fprint(w, `{"id":550,"imdb_id":"tt0137523","title":"Fight Club","overview":"Evidence"}`)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			got, err := tmdb.NewClient("", tmdb.WithBaseURL(server.URL)).FindMovieByExternalID(context.Background(), tc.namespace, tc.id)
			if err != nil || got == nil || got.TMDBID != 550 || finds.Load() != 1 || details.Load() != 1 {
				t.Fatalf("valid identity failed: %#v err=%v calls=%d/%d", got, err, finds.Load(), details.Load())
			}
		})
	}
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{304, "", ports.ErrBadResponse}, {401, "", ports.ErrAuthRequired}, {404, "", ports.ErrNotFound}, {503, "", ports.ErrUnavailable}, {200, "not json", ports.ErrBadResponse},
	} {
		t.Run(fmt.Sprintf("http_%d_%s", tc.status, tc.body), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			got, err := tmdb.NewClient("", tmdb.WithBaseURL(server.URL)).FindMovieByExternalID(context.Background(), "imdb", "tt0137523")
			if got != nil || !errors.Is(err, tc.want) || calls.Load() != 1 {
				t.Fatalf("error class changed: %#v err=%v want=%v calls=%d", got, err, tc.want, calls.Load())
			}
		})
	}
	t.Run("rate_limit", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "12")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		got, err := tmdb.NewClient("", tmdb.WithBaseURL(server.URL)).FindMovieByExternalID(context.Background(), "imdb", "tt0137523")
		rate, ok := ports.IsRateLimited(err)
		if got != nil || !ok || rate.RetryAfter != 12*time.Second || calls.Load() != 1 {
			t.Fatalf("429 metadata changed: %#v err=%v rate=%#v calls=%d", got, err, rate, calls.Load())
		}
	})
}
