package universal_rss_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/universal_rss"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFeedExactURLWithoutInventedFacts(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RequestURI()
		_, _ = w.Write([]byte(`<rss><channel><item><title>Movie.2020.1080p</title><link>magnet:?xt=urn:btih:1234</link></item></channel></rss>`))
	}))
	defer server.Close()
	rows, e := universal_rss.NewFeedClient(server.URL+"/private/rss?passkey=private", nil).Search(context.Background(), "ignored", nil, 0, 50)
	if e != nil || len(rows) != 1 || seen != "/private/rss?passkey=private" || rows[0].SizeBytes != 0 || rows[0].Seeders != 0 || rows[0].Leechers != 0 {
		t.Fatal(seen, rows, e)
	}
}
func TestFeedHTTPContractsAndResponseBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		expected error
	}{{"empty", 200, `<rss><channel></channel></rss>`, nil}, {"not RSS", 200, `<html/>`, ports.ErrBadResponse}, {"malformed", 200, `<rss`, ports.ErrBadResponse}, {"auth", 401, "", ports.ErrAuthRequired}, {"unavailable", 503, "", ports.ErrUnavailable}, {"oversize", 200, strings.Repeat("x", (5<<20)+1), ports.ErrBadResponse}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, e := universal_rss.NewFeedClient(server.URL, nil).Search(context.Background(), "", nil, 0, 50)
			if tc.expected == nil {
				if e != nil {
					t.Fatal(e)
				}
			} else if !errors.Is(e, tc.expected) {
				t.Fatal(e)
			}
		})
	}
}
func TestFeedRateLimitAndRedirectDoesNotForwardSecrets(t *testing.T) {
	var leaked atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"?passkey=secret", http.StatusFound)
	}))
	defer redirect.Close()
	if _, e := universal_rss.NewFeedClient(redirect.URL, nil).Search(context.Background(), "", nil, 0, 50); !errors.Is(e, ports.ErrBadResponse) || leaked.Load() != 0 {
		t.Fatal(e, leaked.Load())
	}
	rate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
	defer rate.Close()
	_, e := universal_rss.NewFeedClient(rate.URL, nil).Search(context.Background(), "", nil, 0, 50)
	var re *ports.RateLimitError
	if !errors.As(e, &re) || re.RetryAfter != 2*time.Minute {
		t.Fatal(e)
	}
}
