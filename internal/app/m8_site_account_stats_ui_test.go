package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

// Explicitly gated: Main owns execution, Chrome, PG and screenshot review.
func TestM8SiteAccountStatsUI(t *testing.T) {
	if os.Getenv("PULSE_M8_STATS_UI") != "1" {
		t.Skip("use scripts/verify_m8_site_account_stats_ui.sh")
	}
	repo, assets, shots := os.Getenv("PULSE_M8_STATS_UI_REPO"), os.Getenv("PULSE_M8_STATS_UI_ASSETS"), os.Getenv("PULSE_M8_STATS_UI_SHOTS")
	for _, p := range []string{assets, shots, os.TempDir()} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/data/temp/") {
			t.Fatal("owned /data/temp path required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repository path required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal("isolated frontend build missing")
	}
	v := m8NewStatsRuntimeFixture(t, false)
	protected := v.protected(t)
	v.saveBeforeRun(t)
	// Explicit setup through the real management service, outside browser traffic.
	key, err := crypto.EnsureMasterKey(v.runtime.cfg.DataDir, v.runtime.cfg.SecretKey)
	if err != nil {
		t.Fatal("owned master key unavailable")
	}
	protector := crypto.NewSecretProtector(key)
	manager := search.NewIndexerManagementService(postgres.NewIndexerManagementStore(v.pool, v.app.IDGen(), protector), protector)
	actor, err := v.auth.AuthenticateToken(v.ctx, v.cookie.Value)
	if err != nil {
		t.Fatal("fixture admin authentication failed")
	}
	sourceURL := "http://" + v.runtime.client.Transport.(m8StatsLocalTransport).host + "/pt/"
	credential := "owned-account-fixture=synthetic"
	unknown, err := manager.Create(v.ctx, actor, ports.IndexerManagementInput{Name: "Owned unconfigured account", Protocol: ports.IndexerProtocolNexusPHP, URL: &sourceURL, Credential: &credential, Categories: []int{}}, "stats-ui-unconfigured-source")
	if err != nil {
		t.Fatal("explicit unconfigured source setup failed")
	}
	nextCookie := v.login(t, "scrape-admin")
	v.start(t)
	v.success(t)
	nativeBefore := v.statsCalls.Load()
	target, err := url.Parse(v.base)
	if err != nil {
		t.Fatal("invalid owned App address")
	}
	proxy := &httputil.ReverseProxy{FlushInterval: -1, Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = target.Host
		if pr.In.Header.Get("Origin") == "http://"+pr.In.Host {
			pr.Out.Header.Set("Origin", v.base)
		}
	}}
	var writes atomic.Int32
	files := http.FileServer(http.Dir(assets))
	// Same fixture-only isolated-assets pattern as M6. Every /api/ goes to App.Run.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			auth := r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/logout" || r.URL.Path == "/api/v1/auth/setup"
			if !auth && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
				writes.Add(1)
			}
			proxy.ServeHTTP(w, r)
			return
		}
		// Explicit local fixture commands: never production routes or invented auth state.
		if r.URL.Path == "/owned/auth-login" && r.Method == "POST" {
			http.SetCookie(w, nextCookie)
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/owned/demote" && r.Method == "POST" {
			result, e := v.pool.Exec(r.Context(), `UPDATE app_user SET role='member' WHERE id=$1 AND role='admin'`, v.admin)
			if e != nil || result.RowsAffected() != 1 {
				t.Error("explicit role setup failed")
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 95*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/m8_site_account_stats_ui.mjs"))
	// Give Playwright's SIGINT handler time to close its owned Chrome before force kill.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	setup, err := json.Marshal(map[string]any{"base": server.URL, "sourceID": v.source.ID.String(), "sourceVersion": v.source.ConfigVersion, "unknownID": unknown.Indexer.ID.String(), "cookie": v.cookie.Value})
	if err != nil {
		t.Fatal("owned browser setup encoding failed")
	}
	cmd.Env = append(os.Environ(), "PULSE_M8_STATS_UI_SETUP="+string(setup))
	output, runErr := cmd.CombinedOutput()
	// Drain actual workers before checking the protected graph and before PG cleanup.
	v.stop(t)
	if runErr != nil {
		t.Fatalf("stats UI browser failed (%v); safe diagnostic: %s", runErr, output)
	}
	for _, marker := range []string{"M8_STATS_UI_BROWSER_PASS", "M8_STATS_UI_CHROME_CLEANUP_VERIFIED"} {
		if strings.Count(string(output), marker) != 1 {
			t.Fatal("missing browser completion receipt")
		}
	}
	if writes.Load() != 0 || v.statsCalls.Load() != nativeBefore || v.unexpected.Load() != 0 {
		t.Fatal("read-only UI changed native calls or issued business writes")
	}
	if protected != v.protected(t) {
		t.Fatal("stats UI changed protected business graph")
	}
	t.Log(strings.TrimSpace(string(output)))
	t.Logf("M8_STATS_UI_APP_PASS real_App=true native_stats_GET=%d health_probe_GET=%d business_mutations=0 protected_graph=unchanged", v.statsCalls.Load(), v.probeCalls.Load())
}
