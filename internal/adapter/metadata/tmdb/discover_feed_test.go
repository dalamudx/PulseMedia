package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestDiscoverFeedRoutes(t *testing.T) {
	for _, tc := range []struct{ category, path, kind string }{
		{"trending", "/trending/all/day", "tv"},
		{"movie", "/movie/popular", "movie"},
		{"tv", "/tv/popular", "tv"},
		{"top_rated", "/movie/top_rated", "movie"},
		{"now_playing", "/movie/now_playing", "movie"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.URL.Query().Get("page") != "2" || r.URL.Query().Get("language") != "zh-CN" || r.URL.Query().Get("api_key") != "owned-key" {
					t.Error("wrong native list route or parameters")
				}
				kind := ""
				if tc.category == "trending" {
					kind = tc.kind
				}
				_, _ = fmt.Fprintf(w, `{"results":[{"id":7,"media_type":%q,"title":"Movie","name":"Show","release_date":"2026-01-01","first_air_date":"2026-01-01","vote_average":7,"genre_ids":[18],"poster_path":"/poster.jpg"}]}`, kind)
			}))
			defer server.Close()
			client := NewClient("owned-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			items, err := client.GetDiscoverFeed(t.Context(), tc.category, 2)
			if err != nil || len(items) != 1 || items[0].MediaType != tc.kind || items[0].TMDBID != 7 || items[0].Year != 2026 || items[0].Rating != 7 || len(items[0].Genres) != 1 || items[0].PosterURL != imageBaseURL+"/poster.jpg" {
				t.Fatalf("lost provider list facts: %#v, %v", items, err)
			}
		})
	}
}

func TestDiscoverFeedAdmissionAndEmpty(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()
	client := NewClient("owned-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	for _, input := range []struct {
		category string
		page     int
	}{{"unknown", 1}, {"movie", -1}, {"movie", 501}} {
		if _, err := client.GetDiscoverFeed(t.Context(), input.category, input.page); !errors.Is(err, ports.ErrRejected) {
			t.Fatalf("invalid list admitted: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request performed I/O")
	}
	items, err := client.GetDiscoverFeed(t.Context(), "now_playing", 0)
	if err != nil || items == nil || len(items) != 0 || calls.Load() != 1 {
		t.Fatalf("valid empty feed confused with failure: %#v, %v", items, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.GetDiscoverFeed(ctx, "movie", 1); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancelled feed performed I/O: %v", err)
	}
}

func TestDiscoverFeedMalformedIsNotEmpty(t *testing.T) {
	for _, tc := range []struct{ name, category, body string }{
		{"missing", "movie", `{}`},
		{"null", "movie", `{"results":null}`},
		{"invalid-json", "movie", `{"results":`},
		{"missing-id", "movie", `{"results":[{"title":"Movie"}]}`},
		{"missing-title", "movie", `{"results":[{"id":1}]}`},
		{"wrong-namespace", "movie", `{"results":[{"id":1,"media_type":"tv","name":"Show"}]}`},
		{"unknown-namespace", "trending", `{"results":[{"id":1,"media_type":"collection","title":"Movie"}]}`},
		{"untyped-trending", "trending", `{"results":[{"id":1,"title":"Movie"}]}`},
		{"date", "movie", `{"results":[{"id":1,"title":"Movie","release_date":"2026-02-30"}]}`},
		{"rating", "movie", `{"results":[{"id":1,"title":"Movie","vote_average":11}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			client := NewClient("owned-key", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			if items, err := client.GetDiscoverFeed(t.Context(), tc.category, 1); !errors.Is(err, ports.ErrBadResponse) || items != nil {
				t.Fatalf("bad feed became usable/empty: %#v, %v", items, err)
			}
		})
	}
}
