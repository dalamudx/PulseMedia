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
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestMovieDetailsCacheDoesNotExposeMutableEvidence(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, `{"id":550,"title":"Original","overview":"Evidence","genres":[{"name":"Drama"}]}`)
	}))
	defer server.Close()
	client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL))
	first, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	first.TMDBID = 999
	first.Title = "Changed"
	first.Genres[0] = "Changed"
	next, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if next.TMDBID != 550 || next.Title != "Original" || len(next.Genres) != 1 || next.Genres[0] != "Drama" || requests.Load() != 1 {
		t.Fatalf("cache evidence altered: %#v requests=%d", next, requests.Load())
	}
}

func TestMovieDetailsFallbackRevalidatesItsOwnLanguage(t *testing.T) {
	var english atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("language") == "zh-CN" {
			if r.Header.Get("If-None-Match") != "" {
				if r.Header.Get("If-None-Match") != `"zh-v1"` {
					t.Error("cross-language validator")
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"zh-v1"`)
			_, _ = fmt.Fprint(w, `{"id":550,"title":"中文标题","overview":""}`)
			return
		}
		n := english.Add(1)
		w.Header().Set("ETag", fmt.Sprintf(`"en-v%d"`, n))
		_, _ = fmt.Fprintf(w, `{"id":550,"title":"English","overview":"English %d"}`, n)
	}))
	defer server.Close()
	c := clock.NewMock(time.Now())
	client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL), tmdb.WithClock(c))
	first, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
	if err != nil || first.Overview != "English 1" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	c.Advance(25 * time.Hour)
	next, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if next.Title != "中文标题" || next.Overview != "English 2" || english.Load() != 2 {
		t.Fatalf("fallback stale after locale 304: %#v english=%d", next, english.Load())
	}
}

func TestMovieDetailsRejectsWrongResponseIdentity(t *testing.T) {
	for _, body := range []string{`{"id":999,"title":"Wrong","overview":"Evidence"}`, `{"id":0}`, `{}`, `null`} {
		t.Run(body, func(t *testing.T) {
			var valid atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if valid.Load() {
					_, _ = fmt.Fprint(w, `{"id":550,"title":"Correct","overview":"Evidence"}`)
				} else {
					_, _ = fmt.Fprint(w, body)
				}
			}))
			defer server.Close()
			client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL))
			value, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
			if !errors.Is(err, ports.ErrBadResponse) || value != nil {
				t.Fatalf("invalid identity accepted: %#v err=%v", value, err)
			}
			valid.Store(true)
			value, err = client.GetMovieDetails(context.Background(), 550, "zh-CN")
			if err != nil || value.TMDBID != 550 {
				t.Fatalf("bad response poisoned cache: %#v err=%v", value, err)
			}
		})
	}
}

func TestMovieDetailsLateResponseCannotOverwriteNewerResponse(t *testing.T) {
	for _, status := range []int{http.StatusNotModified, http.StatusOK} {
		t.Run(fmt.Sprintf("%d", status), func(t *testing.T) {
			var requests atomic.Int32
			entered := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch requests.Add(1) {
				case 1:
					w.Header().Set("ETag", `"v1"`)
					_, _ = fmt.Fprint(w, `{"id":550,"title":"Old","overview":"Old"}`)
				case 2:
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if status == http.StatusNotModified {
						w.WriteHeader(status)
					} else {
						w.Header().Set("ETag", `"old-v2"`)
						_, _ = fmt.Fprint(w, `{"id":550,"title":"Old","overview":"Old"}`)
					}
				default:
					w.Header().Set("ETag", `"v2"`)
					_, _ = fmt.Fprint(w, `{"id":550,"title":"New","overview":"New"}`)
				}
			}))
			defer server.Close()
			defer close(release)
			c := clock.NewMock(time.Now())
			client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL), tmdb.WithClock(c))
			if _, err := client.GetMovieDetails(context.Background(), 550, "zh-CN"); err != nil {
				t.Fatal(err)
			}
			c.Advance(25 * time.Hour)
			done := make(chan error, 1)
			go func() { _, err := client.GetMovieDetails(context.Background(), 550, "zh-CN"); done <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("conditional request did not start")
			}
			value, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
			if err != nil || value.Title != "New" {
				t.Fatalf("new response=%#v err=%v", value, err)
			}
			release <- struct{}{}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("conditional request did not finish")
			}
			value, err = client.GetMovieDetails(context.Background(), 550, "zh-CN")
			if err != nil || value.Title != "New" || requests.Load() != 3 {
				t.Fatalf("old 304 restored stale cache: %#v err=%v requests=%d", value, err, requests.Load())
			}
		})
	}
}

func TestMovieDetailsConditionalCacheContracts(t *testing.T) {
	t.Run("ttl_boundary_and_updated_validator", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch calls.Add(1) {
			case 1:
				w.Header().Set("ETag", `"v1"`)
				_, _ = fmt.Fprint(w, `{"id":550,"title":"Title","overview":"Evidence","genres":[{"name":"Drama"}]}`)
			case 2:
				if r.Header.Get("If-None-Match") != `"v1"` {
					t.Error("missing original validator")
				}
				w.Header().Set("ETag", `"v2"`)
				w.WriteHeader(http.StatusNotModified)
			default:
				if r.Header.Get("If-None-Match") != `"v2"` {
					t.Error("304 validator update lost")
				}
				w.WriteHeader(http.StatusNotModified)
			}
		}))
		defer server.Close()
		c := clock.NewMock(time.Now())
		client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL), tmdb.WithClock(c))
		get := func() *ports.MovieMetadata {
			t.Helper()
			m, err := client.GetMovieDetails(context.Background(), 550, "")
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
		get()
		c.Advance(24*time.Hour - time.Nanosecond)
		get()
		if calls.Load() != 1 {
			t.Fatal("cache expired early")
		}
		c.Advance(time.Nanosecond)
		m := get()
		if calls.Load() != 2 {
			t.Fatal("exact TTL boundary accepted")
		}
		m.Title = "Changed"
		m.Genres[0] = "Changed"
		if m = get(); m.Title != "Title" || m.Genres[0] != "Drama" || calls.Load() != 2 {
			t.Fatal("304 result exposed cache or failed to extend TTL")
		}
		c.Advance(24 * time.Hour)
		get()
		if calls.Load() != 3 {
			t.Fatal("extended TTL did not expire")
		}
	})
	t.Run("unsolicited_304", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) }))
		defer server.Close()
		client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL))
		m, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
		if m != nil || !errors.Is(err, ports.ErrBadResponse) {
			t.Fatalf("uncached 304 accepted: %#v %v", m, err)
		}
	})
	t.Run("failed_fallback_can_recover_without_primary_refetch", func(t *testing.T) {
		var primary, english atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("language") == "zh-CN" {
				primary.Add(1)
				_, _ = fmt.Fprint(w, `{"id":550,"title":"中文标题","overview":""}`)
				return
			}
			if english.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(w, `{"id":550,"title":"English","overview":"Recovered"}`)
		}))
		defer server.Close()
		client := tmdb.NewClient("", tmdb.WithBaseURL(server.URL))
		m, err := client.GetMovieDetails(context.Background(), 550, "zh-CN")
		if err != nil || m.Overview != "" {
			t.Fatalf("failed fallback fabricated evidence: %#v %v", m, err)
		}
		m, err = client.GetMovieDetails(context.Background(), 550, "zh-CN")
		if err != nil || m.Title != "中文标题" || m.Overview != "Recovered" || primary.Load() != 1 || english.Load() != 2 {
			t.Fatalf("fallback retry failed: %#v %v", m, err)
		}
	})
}
