package transmission

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTorrentCompletionUsesActualBytes(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown-bytes", true: "exact-bytes"}[present], func(t *testing.T) {
			hash := strings.Repeat("a", 40)
			const size = int64(8000000003)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				torrent := map[string]any{"hashString": hash, "name": "Fixture", "downloadDir": "/configured", "percentDone": 1, "totalSize": size, "status": 6}
				if present {
					torrent["haveValid"] = size
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]any{"torrents": []any{torrent}}})
			}))
			defer srv.Close()
			c, _ := NewClient(srv.URL, "", "", nil)
			v, e := c.GetTorrent(context.Background(), hash)
			if e != nil || v.State != "seeding" || v.TotalBytes != size {
				t.Fatal(v, e)
			}
			if present && v.DownloadedBytes != size || !present && v.DownloadedBytes != 0 {
				t.Fatal("downloaded bytes were guessed/rounded", v)
			}
		})
	}
}
