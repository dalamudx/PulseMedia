package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
)

func TestM6ApplicationShellActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M6_SHELL_BROWSER") != "1" {
		t.Skip("explicit isolated shell browser fixture required")
	}
	assets, shots := os.Getenv("PULSE_M6_SHELL_ASSETS"), os.Getenv("PULSE_M6_SHELL_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), "/data/temp/") {
			t.Fatal("task-owned paths required")
		}
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f := organizationSetup(t)
	ctx := t.Context()
	var version int
	if err := f.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version != 180006 {
		t.Fatal("PostgreSQL 18.6 required")
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	const username = "shell-admin-with-long-name"
	const password = username + "-Owned-123!"
	if _, _, err = authService.SetupAdmin(ctx, username, password); err != nil {
		t.Fatal(err)
	}
	_, session, err := authService.Login(ctx, username, password)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	manager := download.NewManager(downloadstore.NewRepository(f.pool, f.gen, key))
	manager.SetClientFactory(NativeDownloadClient)
	syncService := mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	syncService.SetCredentialDecoder(crypto.NewSecretProtector(key))
	syncService.SetServerFactory(NativeMediaServerClient)
	settingsService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(key))
	handler := httpapi.NewServerHandler(application, authService, settingsService, nil, nil, nil, nil, nil, nil, nil, manager, syncService, key)
	router := httpapi.NewRouter(application, handler, authService, nil)
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			router.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			files.ServeHTTP(w, r)
		default:
			http.ServeFile(w, r, filepath.Join(assets, "index.html"))
		}
	}))
	defer server.Close()
	var jobsBefore, tasksBefore int
	if err = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM job),(SELECT count(*) FROM download_task)").Scan(&jobsBefore, &tasksBefore); err != nil {
		t.Fatal(err)
	}
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	command := exec.CommandContext(browserCtx, "node", "tests/m6-shell-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M6_SHELL_BASE="+server.URL, "PULSE_M6_SHELL_SESSION="+session, "PULSE_M6_SHELL_CDP="+cdp)
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if err = command.Run(); err != nil {
		t.Fatalf("actual shell Chrome failed: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "M6_SHELL_BROWSER_PASS") {
		t.Fatal("missing actual shell receipt")
	}
	var jobsAfter, tasksAfter int
	if err = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM job),(SELECT count(*) FROM download_task)").Scan(&jobsAfter, &tasksAfter); err != nil {
		t.Fatal(err)
	}
	if jobsAfter != jobsBefore || tasksAfter != tasksBefore {
		t.Fatal("navigation must not create business jobs or tasks")
	}
	t.Log("M6_SHELL_APP_COOKIE_PG_PASS actual_Chrome=true navigation_writes=0")
}
