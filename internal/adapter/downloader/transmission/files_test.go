package transmission

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTorrentFilesStrictFullInventory(t *testing.T) {
	hash := strings.Repeat("a", 40)
	valid := `{"result":"success","arguments":{"torrents":[{"hashString":"` + hash + `","files":[{"name":"Show.S01E01.mkv","length":32,"bytesCompleted":32},{"name":"extra.txt","length":0,"bytesCompleted":0}],"fileStats":[{"bytesCompleted":32,"wanted":true},{"bytesCompleted":0,"wanted":false}]}]}}`
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{{"full-unwanted", valid, 200, nil}, {"different-hash", strings.Replace(valid, hash, strings.Repeat("b", 40), 1), 200, ports.ErrBadResponse}, {"escape", strings.Replace(valid, "Show.S01E01.mkv", "../a.mkv", 1), 200, ports.ErrBadResponse}, {"missing-size", strings.Replace(valid, `"length":32,`, "", 1), 200, ports.ErrBadResponse}, {"stats-conflict", strings.Replace(valid, `"fileStats":[{"bytesCompleted":32`, `"fileStats":[{"bytesCompleted":31`, 1), 200, ports.ErrBadResponse}, {"missing-wanted", strings.Replace(valid, `,"wanted":true`, "", 1), 200, ports.ErrBadResponse}, {"raw-error", `{"result":"fixture-cookie-secret"}`, 200, ports.ErrBadResponse}, {"oversize", strings.Repeat(" ", (1<<20)+1), 200, ports.ErrBadResponse}, {"redirect", "", 302, ports.ErrBadResponse}, {"server", "", 503, ports.ErrUnavailable}, {"rate", "", 429, ports.NewRateLimitError(0, "fixture")}} {
		t.Run(tc.name, func(t *testing.T) {
			var redirects atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1) }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method    string `json:"method"`
					Arguments struct {
						IDs    []string `json:"ids"`
						Fields []string `json:"fields"`
					} `json:"arguments"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "torrent-get" || len(req.Arguments.IDs) != 1 || req.Arguments.IDs[0] != hash || len(req.Arguments.Fields) != 3 {
					t.Error("wrong full-inventory query")
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
			files, e := c.GetTorrentFiles(context.Background(), hash)
			if tc.want == nil {
				if e != nil || len(files) != 2 || files[1].SizeBytes != 0 {
					t.Fatal("unwanted omitted", files, e)
				}
			} else if tc.name == "rate" {
				if _, ok := ports.IsRateLimited(e); !ok {
					t.Fatal(e)
				}
			} else if !errors.Is(e, tc.want) || len(files) != 0 {
				t.Fatal(files, e)
			}
			if e != nil && strings.Contains(e.Error(), "cookie-secret") {
				t.Fatal("raw RPC result exposed")
			}
			if redirects.Load() != 0 {
				t.Fatal("credential redirect followed")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := NewClient("http://fixture.invalid", "", "", nil)
	if _, e := c.GetTorrentFiles(ctx, hash); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
