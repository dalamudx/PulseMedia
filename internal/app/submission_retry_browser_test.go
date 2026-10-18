package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSubmissionRetryBrowserHarness(t *testing.T) {
	if os.Getenv("PULSE_UI_FIXTURE") != "1" {
		t.Skip("explicit owned browser fixture required")
	}
	runFullMoviePipelineSource(t, true, "", "", func(f *organizationFixture, actor *ports.Actor, op uuid.UUID, a *App) func() {
		ctx := context.Background()
		if _, e := f.pool.Exec(ctx, "UPDATE job SET state='dead',attempts=max_attempts WHERE kind='download_submission' AND payload->>'operation_id'=$1", op.String()); e != nil {
			t.Fatal(e)
		}
		token, hash, e := crypto.GenerateToken("pm_sess_")
		if e != nil {
			t.Fatal(e)
		}
		_, csrf, e := crypto.GenerateToken("pm_csrf_")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.q.CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: uuid.MustParse(actor.ID), TokenHash: hash, CsrfTokenHash: csrf, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); e != nil {
			t.Fatal(e)
		}
		svc := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
		h := httpapi.NewServerHandler(a, svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		hub := httpapi.NewSSEHub()
		router := httpapi.NewRouter(a, h, svc, hub)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/fixture/fail" || r.URL.Path == "/fixture/clear-failure" {
				query := "DROP TRIGGER IF EXISTS retry_browser_fail ON audit_log; DROP FUNCTION IF EXISTS retry_browser_fail()"
				if r.URL.Path == "/fixture/fail" {
					query = "CREATE FUNCTION retry_browser_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='download.submission_retry' THEN RAISE EXCEPTION 'fixture-secret-downloader-url'; END IF; RETURN NEW; END $$; CREATE TRIGGER retry_browser_fail BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION retry_browser_fail()"
				}
				if _, err := f.pool.Exec(r.Context(), query); err != nil {
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(204)
				return
			}
			if r.URL.Path == "/fixture/disable" || r.URL.Path == "/fixture/restore" {
				_, err := f.pool.Exec(r.Context(), "UPDATE app_user SET is_active=$2 WHERE id=$1", uuid.MustParse(actor.ID), r.URL.Path == "/fixture/restore")
				if err != nil {
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(204)
				return
			}
			router.ServeHTTP(w, r)
		}))
		defer server.Close()
		defer hub.Close()
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
		cmd := exec.CommandContext(browserCtx, "node", "tests/submission-retry-browser.mjs")
		cmd.Dir = "../../web"
		cmd.Env = append(os.Environ(), "PULSE_RETRY_UI_BASE="+server.URL, "PULSE_RETRY_UI_OP="+op.String(), "PULSE_RETRY_UI_SESSION="+token)
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("retry browser: %v %s", e, out)
		}
		t.Log(string(out))
		var jobs, receipts, audits int
		var state string
		if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_submission'),(SELECT count(*) FROM idempotency_record WHERE response_body->>'job_id' IS NOT NULL AND response_body->>'operation_id'=$1),(SELECT count(*) FROM audit_log WHERE action='download.submission_retry'),(SELECT state FROM job WHERE kind='download_submission' LIMIT 1)", op.String()).Scan(&jobs, &receipts, &audits, &state); e != nil || jobs != 1 || receipts != 1 || audits != 1 || state != "queued" {
			t.Fatal("browser original queue receipt", jobs, receipts, audits, state, e)
		}
		return nil
	})
}
