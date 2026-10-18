package httpclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/nexusphp"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestCredentialedIndexerOriginAndRedirect(t *testing.T) {
	factories := map[string]func(string, *http.Client) ports.IndexerClient{
		"nexusphp": func(base string, c *http.Client) ports.IndexerClient {
			return nexusphp.NewClient(base, "session=synthetic-cookie", "synthetic-passkey", c)
		},
		"torznab": func(base string, c *http.Client) ports.IndexerClient {
			return torznab.NewClient(base, "synthetic-api-key", c)
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			var foreignHits, validHits atomic.Int32
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignHits.Add(1); w.WriteHeader(200) }))
			defer foreign.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/file" {
					validHits.Add(1)
					_, _ = io.WriteString(w, "synthetic-torrent")
					return
				}
				target := foreign.URL + "/stolen"
				if r.URL.Path == "/same-redirect" {
					target = "/file"
				}
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			}))
			defer origin.Close()
			callbackCalled := false
			injected := origin.Client()
			injected.CheckRedirect = func(*http.Request, []*http.Request) error { callbackCalled = true; return nil }
			client := factory(origin.URL, injected)
			data, err := client.FetchTorrent(context.Background(), origin.URL+"/file?passkey=synthetic-passkey")
			if err != nil || string(data) != "synthetic-torrent" {
				t.Fatalf("same-origin fetch %v", err)
			}
			for _, raw := range []string{foreign.URL + "/file", origin.URL + "/redirect", origin.URL + "/same-redirect", "http://user:synthetic-secret@" + strings.TrimPrefix(origin.URL, "http://") + "/file", origin.URL + "/file#synthetic-secret"} {
				data, err = client.FetchTorrent(context.Background(), raw)
				if !errors.Is(err, ports.ErrBadResponse) || data != nil || strings.Contains(err.Error(), "synthetic-") {
					t.Fatalf("unsafe fetch result %v", err)
				}
			}
			if _, err = client.Search(context.Background(), "synthetic-query", nil, 0, 10); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("search redirect %v", err)
			}
			if foreignHits.Load() != 0 || validHits.Load() != 1 || callbackCalled {
				t.Fatalf("requests escaped boundary: foreign=%d valid=%d callback=%v", foreignHits.Load(), validHits.Load(), callbackCalled)
			}
			_ = injected.CheckRedirect(nil, nil)
			if !callbackCalled {
				t.Fatal("constructor mutated injected client")
			}
		})
	}
}
