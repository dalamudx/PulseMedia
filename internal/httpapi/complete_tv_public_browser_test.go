package httpapi_test

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/google/uuid"
)

// The browser exercises production auth/routes and real native services. Only
// fixture account controls and the task-owned static build live outside App.
func runCompleteTVPublicBrowser(t *testing.T, ctx context.Context, base string, f *completeTVFixture) {
	t.Helper()
	dist := os.Getenv("PULSE_COMPLETE_UI_DIST")
	if dist == "" {
		t.Fatal("explicit owned frontend build required")
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	control := "/__complete_ui_" + f.f.sub.Id.String()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, control+"/") {
			cookie, e := r.Cookie("pulsemedia_session")
			if e != nil || cookie.Value != f.f.admin || (r.Method == http.MethodPost && r.Header.Get("Origin") != "http://"+r.Host) {
				http.Error(w, "fixture access denied", http.StatusForbidden)
				return
			}
			action := strings.TrimPrefix(r.URL.Path, control+"/")
			if action == "facts" && r.Method == http.MethodGet {
				var tracking, tasks, reservations, lanes, requirements, commands int
				if e = f.f.a.DBPool().QueryRow(r.Context(), `SELECT (SELECT count(*) FROM download_task WHERE state='tracking'),(SELECT count(*) FROM download_task),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM job WHERE kind IN ('complete_tv_coverage_download_submission','episode_download_submission'))`).Scan(&tracking, &tasks, &reservations, &lanes, &requirements, &commands); e != nil {
					http.Error(w, "fixture query failed", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]int{"tracking": tracking, "tasks": tasks, "reservations": reservations, "lanes": lanes, "requirements": requirements, "commands": commands})
				return
			}
			if r.Method != http.MethodPost || (action != "demote" && action != "restore") {
				http.NotFound(w, r)
				return
			}
			role := "member"
			if action == "restore" {
				role = "admin"
			}
			if _, e = f.f.a.DBPool().Exec(r.Context(), `UPDATE app_user SET role=$1 WHERE id=$2`, role, uuid.MustParse(f.actor.ID)); e != nil {
				http.Error(w, "fixture update failed", 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	scriptName := "../../web/tests/complete-tv-public-browser.mjs"
	if os.Getenv("PULSE_CHOICE_UI_BROWSER") == "1" {
		scriptName = "../../web/tests/tv-selection-public-browser.mjs"
	}
	script, err := filepath.Abs(scriptName)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(browserCtx, "node", script)
	command.Env = append(os.Environ(), "PULSE_COMPLETE_UI_BASE="+server.URL, "PULSE_COMPLETE_UI_SUB="+f.f.sub.Id.String(), "PULSE_COMPLETE_UI_SESSION="+f.f.admin, "PULSE_COMPLETE_UI_CONTROL="+control)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Run(); err != nil {
		var state, failure string
		diagnosticErr := f.f.a.DBPool().QueryRow(ctx, `SELECT state,coalesce(error,'') FROM job WHERE kind='complete_tv_coverage_download_submission' ORDER BY created_at DESC LIMIT 1`).Scan(&state, &failure)
		t.Logf("complete submission job: available=%t state=%s failure=%s", diagnosticErr == nil, state, failure)
		t.Fatalf("real browser failed: %v\n%s", err, output.String())
	}
	t.Log(output.String())
	var tasks, reservations, lanes int
	if err = f.f.a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM download_task),(SELECT count(*) FROM unit_reservation WHERE state IN ('reserved','dispatched')),(SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL)`).Scan(&tasks, &reservations, &lanes); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || reservations != 2 || lanes != 2 {
		t.Fatalf("browser persistent set: tasks=%d reservations=%d lanes=%d", tasks, reservations, lanes)
	}
}
