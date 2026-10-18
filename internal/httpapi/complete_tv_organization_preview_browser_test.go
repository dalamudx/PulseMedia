package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in real Cookie/App.Run/PG browser test; API results and workers are native.
func TestCompleteTVOrganizationPreviewPGAppBrowser(t *testing.T) {
	if os.Getenv("PULSE_ORGANIZATION_UI_BROWSER") != "1" {
		t.Skip("explicit owned Chrome opt-in required")
	}
	f := newCompleteOrganizationFixture(t)
	cfg := *f.n.f.a.Config()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	_ = listener.Close()
	f.n.exec(t, `UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE state='running'`)
	if err = f.n.f.a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(&cfg, app.WithLogger(logging.NewLogger(io.Discard, "error")))
	if err != nil {
		t.Fatal(err)
	}
	f.n.f.a = a
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(35 * time.Second):
				t.Error("App browser did not drain")
			}
			_ = a.Close()
		})
	}
	defer stop()
	base := "http://" + cfg.Server.HTTPAddr
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	runtimeTVWait(t, func() bool {
		r, e := client.Get(base + "/healthz")
		if e != nil {
			return false
		}
		_ = r.Body.Close()
		return r.StatusCode == 200
	})
	controls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie("pulsemedia_session")
		if e != nil || cookie.Value != f.n.f.admin || r.Method != "POST" || r.Header.Get("Origin") != "http://"+r.Host {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		role := "admin"
		if r.URL.Path == "/demote" {
			role = "member"
		} else if r.URL.Path != "/restore" {
			http.NotFound(w, r)
			return
		}
		if _, e = a.DBPool().Exec(r.Context(), `UPDATE app_user SET role=$1 WHERE id=$2`, role, uuid.MustParse(f.n.actor.ID)); e != nil {
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(204)
	}))
	defer controls.Close()
	dist := os.Getenv("PULSE_COMPLETE_UI_DIST")
	if dist == "" {
		t.Fatal("explicit owned frontend build required")
	}
	if _, e := os.Stat(filepath.Join(dist, "index.html")); e != nil {
		t.Fatal(e)
	}
	target, e := url.Parse(base)
	if e != nil {
		t.Fatal(e)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" || !strings.Contains(filepath.Base(r.URL.Path), ".") {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
	}))
	defer ui.Close()
	command := exec.CommandContext(ctx, "node", "../../web/tests/complete-tv-organization-public-browser.mjs")
	if os.Getenv("PULSE_TEST_TEMP_ROOT") == "" {
		t.Fatal("explicit browser temp root required")
	}
	command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("PULSE_TEST_TEMP_ROOT"), "TMP="+os.Getenv("PULSE_TEST_TEMP_ROOT"), "TEMP="+os.Getenv("PULSE_TEST_TEMP_ROOT"), "PULSE_ORGANIZATION_UI_BASE="+ui.URL, "PULSE_ORGANIZATION_UI_OPERATION="+f.command.OperationID.String(), "PULSE_ORGANIZATION_UI_SESSION="+f.n.f.admin, "PULSE_ORGANIZATION_UI_CONTROL="+controls.URL)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Run(); err != nil {
		t.Fatalf("native browser: %v\n%s", err, output.String())
	}
	t.Log(output.String())
	var plan, job uuid.UUID
	if err = a.DBPool().QueryRow(ctx, `SELECT t.transfer_plan_id,j.id FROM download_task t JOIN job j ON j.payload->>'task_id'=t.id::text AND j.kind='download_organization' WHERE t.id=$1`, f.command.TaskID).Scan(&plan, &job); err != nil {
		t.Fatal(err)
	}
	f.assertImported(t, &ports.CompleteTVOrganizationReceipt{TaskID: f.command.TaskID, PlanID: plan, JobID: job, State: "queued"})
	var count int
	if err = a.DBPool().QueryRow(ctx, `SELECT count(*) FROM job WHERE kind='download_organization'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(fmt.Sprintf("unexpected organization jobs %d", count), err)
	}
}
