package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTVArtworkIdentityNativeNamespace(t *testing.T) {
	for _, bearer := range []bool{false, true} {
		t.Run(fmt.Sprint(bearer), func(t *testing.T) {
			key := "fixture-key"
			if bearer {
				key = "ey" + strings.Repeat("a", 70)
			}
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/tv/1396/external_ids" || r.Header.Get("If-None-Match") != "" {
					t.Error("wrong external identity request")
				}
				if bearer {
					if r.Header.Get("Authorization") != "Bearer "+key || r.URL.RawQuery != "" {
						t.Error("wrong bearer auth")
					}
				} else if r.URL.Query().Get("api_key") != key || len(r.URL.Query()) != 1 {
					t.Error("wrong API key auth")
				}
				_, _ = io.WriteString(w, `{"id":1396,"tvdb_id":81189,"imdb_id":"tt0903747"}`)
			}))
			defer server.Close()
			c := NewClient(key, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			got, err := c.ResolveTVArtworkIdentity(t.Context(), 1396)
			if err != nil || got != (ports.TVArtworkIdentity{TMDBID: 1396, TVDBID: 81189}) || calls.Load() != 1 {
				t.Fatalf("unproven TV namespace: %+v %v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestTVArtworkIdentityRejectsAmbiguousEvidence(t *testing.T) {
	cases := []string{`null`, `[]`, `{}`, `{"id":1397,"tvdb_id":81189}`, `{"id":"1396","tvdb_id":81189}`, `{"id":1396}`, `{"id":1396,"tvdb_id":0}`, `{"id":1396,"tvdb_id":-1}`, `{"id":1396,"tvdb_id":1.5}`, `{"id":1396,"tvdb_id":"81189"}`, `{"id":1396,"tvdb_id":9223372036854775808}`, `{"id":1396,"id":1397,"tvdb_id":81189}`, `{"id":1396,"ID":1396,"tvdb_id":81189}`, `{"id":1396,"tvdb_id":81189,"tvdb_id":81190}`, `{"id":1396,"tvdb_id":81189} {}`, `{"id":1396,"tvdb_id":81189,"x":"` + string([]byte{255}) + `"}`}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := detailFixture(t, raw).ResolveTVArtworkIdentity(t.Context(), 1396)
			if !errors.Is(err, ports.ErrBadResponse) || got != (ports.TVArtworkIdentity{}) {
				t.Fatalf("ambiguous identity: %+v %v", got, err)
			}
		})
	}
	if got, err := detailFixture(t, `{"id":1396,"tvdb_id":null}`).ResolveTVArtworkIdentity(t.Context(), 1396); !errors.Is(err, ports.ErrNotFound) || got != (ports.TVArtworkIdentity{}) {
		t.Fatal("missing identity not kept unknown", got, err)
	}
	if got, err := decodeTVArtworkIdentity([]byte(`{"id":1396,"tvdb_id":9223372036854775807}`), 1396); err != nil || got.TVDBID != 9223372036854775807 {
		t.Fatal("integer precision", got, err)
	}
}

func TestTVArtworkIdentityNetworkBoundaries(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusFound, http.StatusNotModified} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "9")
				w.Header().Set("Location", "https://other.invalid")
				w.WriteHeader(status)
			}))
			defer server.Close()
			c := NewClient("fixture-secret", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
			got, err := c.ResolveTVArtworkIdentity(t.Context(), 1396)
			if got != (ports.TVArtworkIdentity{}) || err == nil || calls.Load() != 1 {
				t.Fatal("unexpected identity or resend", got, err, calls.Load())
			}
			if status == http.StatusTooManyRequests {
				if rate, ok := ports.IsRateLimited(err); !ok || rate.RetryAfter != 9*time.Second {
					t.Fatal("backoff", err)
				}
			}
			if status == http.StatusUnauthorized && !errors.Is(err, ports.ErrAuthRequired) || status == http.StatusNotFound && !errors.Is(err, ports.ErrNotFound) || status == http.StatusServiceUnavailable && !errors.Is(err, ports.ErrUnavailable) || (status == http.StatusFound || status == http.StatusNotModified) && !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal("wrong classification", err)
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	c := NewClient("fixture", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	for _, id := range []int64{0, -1} {
		if _, err := c.ResolveTVArtworkIdentity(t.Context(), id); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ResolveTVArtworkIdentity(ctx, 1396); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid/cancelled lookup reached network")
	}
}
