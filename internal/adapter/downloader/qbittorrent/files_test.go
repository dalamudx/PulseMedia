package qbittorrent

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFullFileInventoryStrictAndNoRedirect(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{{"single", `[{"index":0,"name":"Show.S01E01.mkv","size":32,"progress":1}]`, 200, nil}, {"full-unwanted", `[{"index":0,"name":"Show.S01E01.mkv","size":32,"progress":1},{"index":1,"name":"extra.txt","size":0,"progress":0,"priority":0}]`, 200, nil}, {"missing", `[{"name":"a.mkv","size":32,"progress":1}]`, 200, ports.ErrBadResponse}, {"duplicate", `[{"index":0,"name":"a.mkv","size":32,"progress":1},{"index":0,"name":"b.mkv","size":32,"progress":1}]`, 200, ports.ErrBadResponse}, {"escape", `[{"index":0,"name":"../a.mkv","size":32,"progress":1}]`, 200, ports.ErrBadResponse}, {"noncanonical", `[{"index":0,"name":"a/../b.mkv","size":32,"progress":1}]`, 200, ports.ErrBadResponse}, {"half", `[{"index":0,"name":"a.mkv","size":32,"progress":0.5}]`, 200, nil}, {"rate", "", 429, ports.NewRateLimitError(0, "fixture")}, {"server", "", 503, ports.ErrUnavailable}, {"redirect", "", 302, ports.ErrBadResponse}, {"oversize", strings.Repeat(" ", (1<<20)+1), 200, ports.ErrBadResponse}} {
		t.Run(tc.name, func(t *testing.T) {
			var redirected bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/torrents/files" || r.URL.Query().Get("hash") != strings.Repeat("a", 40) {
					t.Error("wrong inventory request")
				}
				if tc.status == 302 {
					w.Header().Set("Location", target.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, e := NewClient(srv.URL, "fixture", "fixture", nil)
			if e != nil {
				t.Fatal(e)
			}
			c.loggedIn = true
			files, e := c.GetTorrentFiles(context.Background(), strings.Repeat("a", 40))
			if tc.want == nil {
				if e != nil || len(files) == 0 {
					t.Fatal(files, e)
				}
				if tc.name == "full-unwanted" && len(files) != 2 {
					t.Fatal("subset lost")
				}
			} else if tc.name == "rate" {
				if _, ok := ports.IsRateLimited(e); !ok || len(files) != 0 {
					t.Fatal(files, e)
				}
			} else if !errors.Is(e, tc.want) || len(files) != 0 {
				t.Fatal(files, e)
			}
			if redirected {
				t.Fatal("redirect followed")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := NewClient("http://fixture.invalid", "", "", nil)
	c.loggedIn = true
	if _, e := c.GetTorrentFiles(ctx, strings.Repeat("b", 40)); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
