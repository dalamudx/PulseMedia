package universal_rss

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestPublicTorrentSourceBoundary(t *testing.T) {
	var hits, foreignHits atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignHits.Add(1); _, _ = w.Write([]byte("unexpected")) }))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, foreign.URL+"/private", http.StatusFound)
		case "/large":
			_, _ = w.Write([]byte(strings.Repeat("x", (10<<20)+1)))
		default:
			_, _ = w.Write([]byte("torrent fixture bytes"))
		}
	}))
	defer origin.Close()
	client := NewFeedClient(origin.URL+"/rss?passkey=fixture-secret", nil)
	body, err := client.FetchTorrent(t.Context(), origin.URL+"/torrent?passkey=fixture-secret")
	if err != nil || string(body) != "torrent fixture bytes" {
		t.Fatalf("same-source torrent unavailable: %v", err)
	}
	for _, target := range []string{foreign.URL + "/private", origin.URL + "/torrent#fragment", strings.Replace(origin.URL, "http://", "http://name:secret@", 1) + "/torrent"} {
		before := hits.Load()
		if _, err := client.FetchTorrent(t.Context(), target); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatalf("unsafe source accepted: %v", err)
		}
		if hits.Load() != before || foreignHits.Load() != 0 {
			t.Fatal("unsafe URL caused a network call")
		}
	}
	if _, err := client.FetchTorrent(t.Context(), origin.URL+"/redirect"); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("redirect accepted: %v", err)
	}
	if foreignHits.Load() != 0 {
		t.Fatal("redirect followed to unconfigured source")
	}
	if _, err := client.FetchTorrent(t.Context(), origin.URL+"/large"); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("oversized torrent accepted: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	before := hits.Load()
	for _, target := range []string{origin.URL + "/torrent", "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"} {
		if _, err := client.FetchTorrent(canceled, target); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	if hits.Load() != before {
		t.Fatal("canceled source performed network I/O")
	}
}

type securityTransportFunc func(*http.Request) (*http.Response, error)

func (f securityTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicFeedTransportErrorsDoNotExposeURLs(t *testing.T) {
	client := NewFeedClient("https://source.invalid/rss?passkey=fixture-secret", &http.Client{Transport: securityTransportFunc(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("request contains fixture-secret") })})
	_, err := client.Search(t.Context(), "", nil, 0, 1)
	if !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe feed error: %v", err)
	}
	_, err = client.FetchTorrent(t.Context(), "https://source.invalid/torrent?passkey=fixture-secret")
	if !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe torrent error: %v", err)
	}
}
