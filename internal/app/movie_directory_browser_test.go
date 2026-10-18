package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestMovieDirectoryActivityBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit private browser fixture required")
	}
	runFullMoviePipelineSource(t, true, "root", "browser")
}
func runMovieDirectoryActivityBrowser(t *testing.T, ctx context.Context, a *App, f *organizationFixture, user, op uuid.UUID) {
	t.Helper()
	token, hash, e := crypto.GenerateToken("pm_sess_")
	if e != nil {
		t.Fatal(e)
	}
	_, csrf, e := crypto.GenerateToken("pm_csrf_")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: user, TokenHash: hash, CsrfTokenHash: csrf, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
		t.Fatal(e)
	}
	authSvc := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	handler := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	hub := httpapi.NewSSEHub()
	router := httpapi.NewRouter(a, handler, authSvc, hub)
	if os.Getenv("PULSE_INBOX_UI_FIXTURE") == "1" {
		router = activityInboxBrowserRouter(t, ctx, f, user, op, hub, router)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	chrome := exec.CommandContext(browserCtx, "/opt/google/chrome/chrome", "--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--remote-debugging-port=9222", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome"))
	chrome.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	chrome.Cancel = func() error { return syscall.Kill(-chrome.Process.Pid, syscall.SIGKILL) }
	chrome.Stdout = io.Discard
	chrome.Stderr = io.Discard
	if e = chrome.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = syscall.Kill(-chrome.Process.Pid, syscall.SIGKILL); _ = chrome.Wait() }()
	script := "tests/movie-directory-browser.mjs"
	if os.Getenv("PULSE_INBOX_UI_FIXTURE") == "1" {
		script = "tests/operation-inbox-browser.mjs"
	}
	command := exec.CommandContext(browserCtx, "node", script)
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_DIRECTORY_UI_BASE="+server.URL, "PULSE_DIRECTORY_UI_OP="+op.String(), "PULSE_DIRECTORY_UI_SESSION="+token)
	out, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("directory UI: %v %s", e, out)
	}
	t.Log(string(out))
}
