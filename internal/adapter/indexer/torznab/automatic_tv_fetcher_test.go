package torznab_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestAutomaticTVTorrentFetcherOriginalBytesAndCredentials(t *testing.T) {
	raw := []byte("original metainfo bytes\x00private")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "configured-user" || password != "configured-password" || r.Method != http.MethodGet || r.URL.RequestURI() != "/original%2Ffile?token=exact%2fvalue&b=2&a=1" {
			t.Error("original URL or configured credentials changed")
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL + "/private/api?apikey=endpoint-secret")
	if err != nil {
		t.Fatal(err)
	}
	endpoint.User = url.UserPassword("configured-user", "configured-password")
	fetcher := torznab.NewAutomaticTVTorrentFetcher(server.Client())
	got, err := fetcher.FetchAutomaticTVTorrent(t.Context(), ports.AutomaticTVTorrentSource{EndpointURL: endpoint.String(), APIKey: "separate-secret", DownloadURL: server.URL + "/original%2Ffile?token=exact%2fvalue&b=2&a=1"})
	if err != nil || !bytes.Equal(got, raw) || calls.Load() != 1 {
		t.Fatalf("fetch bytes changed or request count=%d: %v", calls.Load(), err)
	}
}

func TestAutomaticTVTorrentFetcherRejectsUnsafeOrUnboundedResponse(t *testing.T) {
	for _, scenario := range []string{"redirect", "foreign-origin", "candidate-userinfo", "too-large", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if scenario == "redirect" {
					http.Redirect(w, r, "/redirected", http.StatusFound)
					return
				}
				_, _ = io.Copy(w, io.LimitReader(zeroAutomaticTVReader{}, (10<<20)+1))
			}))
			defer server.Close()
			target := server.URL + "/torrent?passkey=private-secret"
			ctx := t.Context()
			if scenario == "foreign-origin" {
				target = "https://foreign.invalid/torrent?passkey=private-secret"
			}
			if scenario == "candidate-userinfo" {
				u, _ := url.Parse(target)
				u.User = url.UserPassword("bad", "private-secret")
				target = u.String()
			}
			if scenario == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			_, err := torznab.NewAutomaticTVTorrentFetcher(server.Client()).FetchAutomaticTVTorrent(ctx, ports.AutomaticTVTorrentSource{EndpointURL: server.URL + "/api", DownloadURL: target})
			expected := ports.ErrBadResponse
			if scenario == "cancelled" {
				expected = context.Canceled
			}
			if !errors.Is(err, expected) {
				t.Fatalf("unsafe fetch returned %v", err)
			}
			want := int32(0)
			if scenario == "redirect" || scenario == "too-large" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("fetch calls=%d want %d", calls.Load(), want)
			}
		})
	}
}

type zeroAutomaticTVReader struct{}

func (zeroAutomaticTVReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type automaticTVFetchRoundTrip func(*http.Request) (*http.Response, error)

func (f automaticTVFetchRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAutomaticTVTorrentFetcherCapsDeadlineWithoutMutatingClient(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Hour} {
		t.Run(timeout.String(), func(t *testing.T) {
			client := &http.Client{Timeout: timeout, Transport: automaticTVFetchRoundTrip(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Error("native fetch exceeded 15 second budget")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("original"))), Request: r}, nil
			})}
			got, err := torznab.NewAutomaticTVTorrentFetcher(client).FetchAutomaticTVTorrent(t.Context(), ports.AutomaticTVTorrentSource{EndpointURL: "https://source.invalid/api", DownloadURL: "https://source.invalid/download"})
			if err != nil || string(got) != "original" || client.Timeout != timeout || client.CheckRedirect != nil {
				t.Fatal("fetch budget or caller client changed", err)
			}
		})
	}
}
