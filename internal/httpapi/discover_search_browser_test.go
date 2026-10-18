package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Discovery metadata here is a deliberate UI fixture, not a provider acceptance test.
func TestDiscoverSearchBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	a, _, _, _, _, _, router, _ := setupOperationHTTP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	var good, bad atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		good.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<rss><channel><item><title>Discover.Fixture.2020.1080p.WEB-DL.x264-GROUP</title><guid>discover-real</guid><link>http://fixture.invalid/discover.torrent</link><size>4000000000</size></item></channel></rss>`)
	}))
	defer source.Close()
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { bad.Add(1); w.WriteHeader(503) }))
	defer failed.Close()
	cipher, e := crypto.Encrypt(make([]byte, 32), []byte("fixture-discover-key"))
	if e != nil {
		t.Fatal(e)
	}
	for _, url := range []string{source.URL, failed.URL} {
		if _, e = a.DBPool().Exec(ctx, "INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'Discover source',$2,$3,'[]','online')", uuid.New(), url, cipher); e != nil {
			t.Fatal(e)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/discover" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"tmdb_id": 991, "title": "Discover Fixture", "year": 2020, "media_type": "movie"}, {"tmdb_id": 992, "title": "Other Fixture", "year": 2021, "media_type": "movie"}}})
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer server.Close()
	script, e := filepath.Abs("../../web/tests/discover-search-browser.mjs")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_DISCOVER_SEARCH_BASE="+server.URL)
	raw, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("discovery search browser failed: %v %s", e, raw)
	}
	t.Log(string(raw))
	if good.Load() < 1 || bad.Load() < 1 {
		t.Fatal("production search did not contact both protocol sources", good.Load(), bad.Load())
	}
	var jobs int
	if e = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&jobs); e != nil || jobs != 0 {
		t.Fatal("read-only probe submitted download", jobs, e)
	}
}
