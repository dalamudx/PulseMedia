package torznab_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTorznabConfiguredCredentialsAndQuery(t *testing.T) {
	cases := []struct{ name, endpoint, path, key, wantKey string }{
		{"root", "/private?passkey=kept%2Fvalue", "/private/api", "separate-key", "separate-key"},
		{"complete API", "/private/api?passkey=kept%2Fvalue", "/private/api", "separate-key", "separate-key"},
		{"escaped path", "/encoded%2Fsegment/api?passkey=kept%2Fvalue", "/encoded%2Fsegment/api", "separate-key", "separate-key"},
		{"URL API key", "/private/api?apikey=url-key&passkey=kept%2Fvalue", "/private/api", "", "url-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				user, secret, ok := r.BasicAuth()
				if !ok || user != "configured-user" || secret != "configured-secret" {
					t.Error("configured authentication missing")
				}
				if r.URL.Path == "/torrent" {
					_, _ = io.WriteString(w, "synthetic torrent bytes")
					return
				}
				q := r.URL.Query()
				if r.URL.EscapedPath() != tc.path || q.Get("passkey") != "kept/value" || q.Get("apikey") != tc.wantKey {
					t.Error("configured path or query changed")
				}
				switch q.Get("t") {
				case "caps":
					_, _ = io.WriteString(w, `<caps><searching><movie-search available="yes" supportedParams="q"/></searching></caps>`)
				case "movie":
					if q.Get("q") != "Movie title" {
						t.Error("search query missing")
					}
					_, _ = io.WriteString(w, `<rss><channel><response offset="0" total="0"/></channel></rss>`)
				default:
					t.Error("protocol action missing")
				}
			}))
			defer origin.Close()
			endpoint, err := url.Parse(origin.URL + tc.endpoint)
			if err != nil {
				t.Fatal("fixture URL failed")
			}
			endpoint.User = url.UserPassword("configured-user", "configured-secret")
			client := torznab.NewClient(endpoint.String(), tc.key, origin.Client())
			if _, err = client.Capabilities(context.Background()); err != nil {
				t.Fatal("configured capabilities failed", err)
			}
			page, err := client.SearchPage(context.Background(), "Movie title", nil, 0, 1)
			if err != nil || page == nil || len(page.Items) != 0 {
				t.Fatal("configured search failed", err)
			}
			if _, err = client.FetchTorrent(context.Background(), origin.URL+"/torrent"); err != nil {
				t.Fatal("same-origin download failed", err)
			}
			forbidden := *endpoint
			forbidden.Path = "/torrent"
			forbidden.RawQuery = ""
			forbidden.RawPath = ""
			if _, err = client.FetchTorrent(context.Background(), forbidden.String()); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal("candidate userinfo accepted")
			}
			if calls.Load() != 3 {
				t.Fatal("unexpected HTTP execution")
			}
		})
	}
}
