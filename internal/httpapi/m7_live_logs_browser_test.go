package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestM7LiveLogsActualChromeHTTPPG drives the settings log viewer against the
// real App, Cookie session and PostgreSQL. Only GET requests are permitted.
func TestM7LiveLogsActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M7_LIVE_LOGS_BROWSER") != "1" {
		t.Skip("use scripts/verify_m7_live_logs.sh")
	}
	repo, assets, shots := os.Getenv("PULSE_M7_LIVE_LOGS_REPO"), os.Getenv("PULSE_M7_LIVE_LOGS_ASSETS"), os.Getenv("PULSE_M7_LIVE_LOGS_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.Getenv("TMPDIR")} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), "/data/temp/") {
			t.Fatal("owned /data/temp paths required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repo required")
	}
	t.Setenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE", "1")
	f, _ := newM5CreationAcceptance(t)

	const marker = "owned-live-log-marker"
	const secret = "live-log-owned-secret"
	// Publish through the process logger so the App's own hub wiring is used.
	f.app.Logger().Warn(marker, slog.String("url", "https://user:"+secret+"@example.invalid/?api_key="+secret), slog.String("attempt", "1"))

	target, _ := url.Parse(f.base)
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(target)
		p.Out.Host = target.Host
		if p.In.Header.Get("Origin") == "http://"+p.In.Host {
			p.Out.Header.Set("Origin", f.base)
		}
	}}
	static := http.FileServer(http.Dir(assets))
	var writes atomic.Int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != http.MethodGet {
				writes.Add(1)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			static.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(bridge.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	cdp, stopChrome := startFileMappingBrowser(t, ctx)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			stopChrome()
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			if response, err := client.Get(cdp + "/json/version"); err == nil {
				_ = response.Body.Close()
				t.Error("Chrome remains reachable")
			} else {
				t.Log("M7_LIVE_LOGS_CHROME_CLEANUP_VERIFIED")
			}
		})
	}
	defer stop()

	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/tests/m7-live-logs-browser.mjs"))
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(os.Environ(),
		"PULSE_M7_LIVE_LOGS_BASE="+bridge.URL,
		"PULSE_M7_LIVE_LOGS_SESSION="+f.session,
		"PULSE_M7_LIVE_LOGS_CDP="+cdp,
		"PULSE_M7_LIVE_LOGS_CHROME="+os.Getenv("PULSE_M7_LIVE_LOGS_CHROME_BIN"),
		"PULSE_M7_LIVE_LOGS_REPO="+repo,
		"PULSE_M7_LIVE_LOGS_SCREENSHOTS="+shots,
		"PULSE_M7_LIVE_LOGS_MARKER="+marker,
		"PULSE_M7_LIVE_LOGS_SECRET="+secret,
	)
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, os.Stdout)
	cmd.Stderr = cmd.Stdout
	runErr := cmd.Run()
	stop()
	if runErr != nil {
		t.Fatalf("actual Chrome failed: %v", runErr)
	}
	if !strings.Contains(output.String(), "M7_LIVE_LOGS_BROWSER_PASS ") {
		t.Fatal("missing browser receipt")
	}
	if writes.Load() != 0 {
		t.Fatalf("log viewing performed %d write requests", writes.Load())
	}
	t.Log("M7_LIVE_LOGS_APP_COOKIE_PG_PASS schema93 PG=18.6 redacted=true level_filter=true paused=true writes=0")
}
