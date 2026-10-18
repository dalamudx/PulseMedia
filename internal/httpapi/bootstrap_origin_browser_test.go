package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapOriginRealBrowser(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit disposable browser fixture required")
	}
	t.Setenv("PULSE_SETUP_TOKEN", "")
	a, _, router := setupAuthHTTP(t)
	ctx := context.Background()
	var now time.Time
	if err := a.DBPool().QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	a.Clock().(*clock.MockClock).Set(now)
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/auth-counts" {
			var users, sessions int
			if err := a.DBPool().QueryRow(r.Context(), "SELECT (SELECT count(*) FROM app_user),(SELECT count(*) FROM app_session)").Scan(&users, &sessions); err != nil {
				http.Error(w, "fixture count unavailable", 500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"users": users, "sessions": sessions})
			return
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(main.Close)
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><title>Origin probe</title>"))
	}))
	t.Cleanup(foreign.Close)
	script, err := filepath.Abs("../../web/tests/bootstrap-origin-browser.mjs")
	if err != nil {
		t.Fatal(err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "node", script)
	cmd.Env = append(os.Environ(), "PULSE_BOOTSTRAP_BROWSER_BASE="+main.URL, "PULSE_BOOTSTRAP_FOREIGN_BASE="+foreign.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap browser failed: %v %s", err, output)
	}
	t.Log(string(output))
}
