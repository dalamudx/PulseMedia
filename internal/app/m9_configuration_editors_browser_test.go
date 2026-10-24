package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
)

func TestM9ConfigurationEditorsChromePG(t *testing.T) {
	if os.Getenv("PULSE_M9_EDITOR_BROWSER") != "1" {
		t.Skip("owned editor Chrome fixture required")
	}
	assets, shots := os.Getenv("PULSE_M9_EDITOR_ASSETS"), os.Getenv("PULSE_M9_EDITOR_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		clean := filepath.Clean(path)
		real, err := filepath.EvalSymlinks(clean)
		if !filepath.IsAbs(path) || !strings.HasPrefix(clean, "/data/temp/") || err != nil || real != clean {
			t.Fatal("owned absolute paths required")
		}
	}
	f := organizationSetup(t)
	ctx := t.Context()
	var version string
	if err := f.pool.QueryRow(ctx, "SHOW server_version_num").Scan(&version); err != nil || version != "180006" {
		t.Fatal("owned PostgreSQL 18.6 required")
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	admin, adminSession, err := authService.SetupAdmin(ctx, "editors-admin", "Owned-Editors-Admin-123!")
	if err != nil {
		t.Fatal("owned administrator", err)
	}
	memberID := uuid.New()
	hash, err := crypto.HashPassword("Owned-Editors-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	m6DashboardExec(t, f, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'editors-member',$2,'member')`, memberID, hash)
	_, memberSession, err := authService.Login(ctx, "editors-member", "Owned-Editors-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	policyService := policy.NewService(postgres.NewPolicyStore(f.pool, f.gen))
	rules := quality.PolicyRules{CutoffScore: 900, ResolutionOrder: map[quality.Resolution]int{"1080p": 100}, SourceOrder: map[quality.Source]int{"WEB-DL": 50}, RejectTerms: []string{}, RejectGroups: []string{}}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID.String(), Role: "admin"}
	if _, err = policyService.Command(ctx, actor, ports.PolicyCommand{Kind: "create", ID: "m9-editors", Name: "可视化编辑验收策略", Rules: &rules}, "owned-editor-seed"); err != nil {
		t.Fatal("formal policy seed", err)
	}
	settingService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(outboxTestKey))
	var nativeCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nativeCalls.Add(1)
		http.Error(w, "unexpected editor network call", http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	provider := tmdb.NewClient("owned-editors", tmdb.WithBaseURL(upstream.URL), tmdb.WithHTTPClient(upstream.Client()))
	handler := httpapi.NewServerHandler(application, authService, settingService, nil, nil, provider, nil, policyService, nil, nil, f.mgr, nil, outboxTestKey)
	router := httpapi.NewRouter(application, handler, authService, nil)
	baseline, err := m6DashboardSnapshot(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	writes, previews, reads := 0, 0, 0
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/__m9_editors/") {
			mu.Lock()
			defer mu.Unlock()
			before, e := m6DashboardSnapshot(r.Context(), f)
			if e != nil || !reflect.DeepEqual(before, baseline) {
				t.Error("editor snapshot changed between requests")
				http.Error(w, "snapshot", http.StatusInternalServerError)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/__m9_editors/") {
				switch r.URL.Path {
				case "/__m9_editors/counters":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"writes": writes, "previews": previews, "reads": reads, "native": nativeCalls.Load()})
				case "/__m9_editors/demote":
					if _, e = f.pool.Exec(r.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, admin.ID); e != nil {
						t.Error(e)
						http.Error(w, "role", http.StatusInternalServerError)
						return
					}
					w.WriteHeader(204)
				case "/__m9_editors/restore":
					if _, e = f.pool.Exec(r.Context(), `UPDATE app_user SET role='admin' WHERE id=$1`, admin.ID); e != nil {
						t.Error(e)
						http.Error(w, "role", http.StatusInternalServerError)
						return
					}
					w.WriteHeader(204)
				default:
					http.NotFound(w, r)
				}
				return
			}
			recorded := httptest.NewRecorder()
			router.ServeHTTP(recorded, r)
			after, e := m6DashboardSnapshot(r.Context(), f)
			if e != nil {
				t.Error(e)
				http.Error(w, "snapshot", http.StatusInternalServerError)
				return
			}
			mutation := r.Method == http.MethodPut && r.URL.Path == "/api/v1/settings/organization.naming_templates" || r.Method == http.MethodPost && r.URL.Path == "/api/v1/policies/m9-editors/revisions"
			preview := r.Method == http.MethodPost && (r.URL.Path == "/api/v1/policies/preview" || r.URL.Path == "/api/v1/organization/naming/preview")
			allowed := map[string]bool{}
			if mutation {
				writes++
				if recorded.Code >= 200 && recorded.Code < 300 {
					for _, table := range []string{"policy", "policy_revision", "setting", "audit_log", "outbox_event", "idempotency_record"} {
						allowed[table] = true
					}
				}
			} else if preview {
				previews++
			} else {
				reads++
			}
			for table, digest := range before {
				if !allowed[table] && after[table] != digest {
					t.Errorf("editor changed protected %s", table)
				}
			}
			if nativeCalls.Load() != 0 {
				t.Error("editor contacted metadata")
			}
			baseline = after
			for key, values := range recorded.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	setup, err := json.Marshal(map[string]any{"base": server.URL, "adminSession": adminSession, "memberSession": memberSession, "owner": admin.ID})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(browserCtx, "node", "tests/m9-configuration-editors-browser.mjs")
	cmd.Dir = "../../web"
	cmd.Env = append(os.Environ(), "PULSE_M9_EDITOR_SETUP="+string(setup), "PULSE_M9_EDITOR_CDP="+cdp)
	cmd.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, os.Stdout)
	cmd.Stderr = cmd.Stdout
	if err = cmd.Run(); err != nil {
		t.Fatalf("configuration editor browser: %v\n%s", err, output.String())
	}
	if strings.Count(output.String(), "M9_EDITOR_CHROME_PASS") != 1 {
		t.Fatal("missing browser receipt")
	}
	var revision int64
	if err = f.pool.QueryRow(ctx, `SELECT current_revision FROM policy WHERE id='m9-editors'`).Scan(&revision); err != nil || revision != 2 {
		t.Error("policy not committed exactly once", revision, err)
	}
	var templateVersion int64
	if err = f.pool.QueryRow(ctx, `SELECT version FROM setting WHERE key='organization.naming_templates'`).Scan(&templateVersion); err != nil || templateVersion != 1 {
		t.Error("template not committed once", templateVersion, err)
	}
	mu.Lock()
	final, e := m6DashboardSnapshot(ctx, f)
	if e != nil || !reflect.DeepEqual(final, baseline) {
		t.Error("final editor read changed rows")
	}
	mu.Unlock()
	if nativeCalls.Load() != 0 || writes != 3 || previews < 5 {
		t.Error("unexpected editor call counts", writes, previews, nativeCalls.Load())
	}
	if os.Getenv("PULSE_M9_EDITOR_REVIEW") == "1" {
		fmt.Printf("M9_EDITOR_SCREENSHOTS_READY dir=%s\n", shots)
		deadline := time.NewTimer(300 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if info, e := os.Stat(filepath.Join(shots, "inspected")); e == nil && info.Mode().IsRegular() {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("review cancelled")
			case <-deadline.C:
				t.Fatal("review receipt missing")
			case <-ticker.C:
			}
		}
	}
	t.Log("M9_EDITOR_NATIVE_COOKIE_CHROME_PASS policy_revision=2 naming_version=1 policy_original_key=REPLAY naming_original_key=GET metadata=0 snapshots=FULL_NON_AUTH workers=NOT_STARTED")
}
