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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/audit"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/personfollow"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func TestM11AdvancedSettingsChromePG(t *testing.T) {
	if os.Getenv("PULSE_M11_ADVANCED_BROWSER") != "1" {
		t.Skip("owned advanced settings Chrome fixture required")
	}
	assets, shots := os.Getenv("PULSE_M11_ADVANCED_ASSETS"), os.Getenv("PULSE_M11_ADVANCED_SHOTS")
	for _, p := range []string{assets, shots, os.TempDir()} {
		real, e := filepath.EvalSymlinks(p)
		if e != nil || real != filepath.Clean(p) || !strings.HasPrefix(p, "/data/temp/") {
			t.Fatal("owned paths required")
		}
	}
	f := organizationSetup(t)
	ctx := t.Context()
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	application, e := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = application.Close() })
	authentication := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	owner, session, e := authentication.SetupAdmin(ctx, "advanced-admin", "Owned-Advanced-Admin-123!")
	if e != nil {
		t.Fatal(e)
	}
	memberID := uuid.New()
	hash, e := crypto.HashPassword("Owned-Advanced-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	m6DashboardExec(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,'advanced-member',$2,'member',true)`, memberID, hash)
	_, member, e := authentication.Login(ctx, "advanced-member", "Owned-Advanced-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: owner.ID.String(), Role: ports.RoleAdmin}
	policies := policy.NewService(postgres.NewPolicyStore(f.pool, f.gen))
	rules := quality.PolicyRules{CutoffScore: 900, ResolutionOrder: map[quality.Resolution]int{"1080p": 100}, SourceOrder: map[quality.Source]int{"WEB-DL": 50}, RejectTerms: []string{}, RejectGroups: []string{}}
	_, e = policies.Command(ctx, actor, ports.PolicyCommand{Kind: "create", ID: "advanced-ui", Name: "人物关注验收策略", Rules: &rules}, "advanced-ui-policy-key")
	if e != nil {
		t.Fatal(e)
	}
	m6DashboardExec(t, f, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, f.root)
	for i := range 53 {
		m6DashboardExec(t, f, `INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload) VALUES($1,'user',$2,'owned.ui.audit','media',$3,'{"api_key":"must-never-be-displayed"}')`, uuid.New(), owner.ID.String(), fmt.Sprint(i))
	}
	settingService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(outboxTestKey))
	handler := httpapi.NewServerHandler(application, authentication, settingService, nil, nil, nil, nil, policies, nil, nil, f.mgr, nil, outboxTestKey)
	handler.SetPersonFollowService(personfollow.NewConfigService(postgres.NewPersonFollowStore(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()))))
	handler.SetAuditTimelineService(audit.NewTimelineService(postgres.NewAuditTimelineStore(f.pool)))
	handler.SetRootSelectionService(subscription.NewRootSelectionService(postgres.NewLibraryRootSelectionStore(f.pool)))
	decoder := crypto.NewSecretProtector(outboxTestKey)
	if e = postgres.EncryptLegacyIndexerConfigurations(ctx, f.pool, decoder, usecaseSearch.ClassifyLegacyIndexerProtocol); e != nil {
		t.Fatal(e)
	}
	indexerStore := postgres.NewIndexerManagementStore(f.pool, f.gen, decoder)
	handler.SetIndexerManagementServices(usecaseSearch.NewIndexerManagementService(indexerStore, crypto.NewSecretProtector(outboxTestKey)), usecaseSearch.NewIndexerManagementReceiptService(indexerStore))
	router := httpapi.NewRouter(application, handler, authentication, nil)
	for _, path := range []string{"/api/v1/library-roots", "/api/v1/policies", "/api/v1/indexers", "/api/v1/download-clients"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		record := httptest.NewRecorder()
		router.ServeHTTP(record, req)
		if record.Code != http.StatusOK {
			t.Fatalf("option preflight %s status=%d", path, record.Code)
		}
	}
	var mu sync.Mutex
	mutations := 0
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/theme.js" || r.URL.Path == "/pulse-push-sw.js" {
				files.ServeHTTP(w, r)
			} else {
				http.ServeFile(w, r, filepath.Join(assets, "index.html"))
			}
			return
		}
		mu.Lock()
		defer mu.Unlock()
		snapshotCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		defer cancel()
		before, err := m6DashboardSnapshot(snapshotCtx, f)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		record := httptest.NewRecorder()
		router.ServeHTTP(record, r)
		after, err := m6DashboardSnapshot(snapshotCtx, f)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		allowed := map[string]bool{}
		if r.Method != http.MethodGet {
			mutations++
			if r.Method != http.MethodPut {
				t.Error("unexpected write", r.Method, r.URL.Path)
			}
			if record.Code == http.StatusOK {
				for _, table := range []string{"audit_log", "outbox_event", "outbox_aggregate_sequence", "idempotency_record"} {
					allowed[table] = true
				}
				if strings.HasPrefix(r.URL.Path, "/api/v1/settings/fanart.") {
					allowed["setting"] = true
				} else if strings.HasPrefix(r.URL.Path, "/api/v1/person-follows/") {
					for _, table := range []string{"person_follow_config", "person_follow_config_history", "person_follow_indexer"} {
						allowed[table] = true
					}
				} else {
					t.Error("unexpected configuration write", r.URL.Path)
				}
			}
		}
		for table, digest := range before {
			if !allowed[table] && after[table] != digest {
				t.Error("protected table changed", table, r.Method, r.URL.Path)
			}
		}
		for k, values := range record.Header() {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(record.Code)
		_, _ = w.Write(record.Body.Bytes())
	}))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	setup, _ := json.Marshal(map[string]any{"base": server.URL, "admin": session, "member": member, "owner": owner.ID, "memberID": memberID, "root": f.root, "policy": "advanced-ui"})
	command := exec.CommandContext(browserCtx, "node", "tests/m11-advanced-settings-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M11_ADVANCED_SETUP="+string(setup), "PULSE_M11_ADVANCED_CDP="+cdp)
	command.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if e = command.Run(); e != nil {
		t.Fatalf("advanced settings Chrome: %v\n%s", e, output.String())
	}
	if strings.Count(output.String(), "M11_ADVANCED_CHROME_PASS") != 1 || mutations != 4 {
		t.Fatal("unexpected browser receipt or mutation count", mutations)
	}
	var follows, jobs int
	if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM person_follow_config),(SELECT count(*) FROM job WHERE kind='person_follow_sync')`).Scan(&follows, &jobs); e != nil || follows != 2 || jobs != 0 {
		t.Fatal("save created execution or missing config", follows, jobs, e)
	}
	if os.Getenv("PULSE_M11_ADVANCED_REVIEW") == "1" {
		fmt.Printf("M11_ADVANCED_SCREENSHOTS_READY dir=%s\n", shots)
		timer := time.NewTimer(600 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, e = os.Stat(filepath.Join(shots, "inspected")); e == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("review cancelled")
			case <-timer.C:
				t.Fatal("review absent")
			case <-ticker.C:
			}
		}
	}
	t.Log("M11_ADVANCED_BROWSER_PG_PASS Cookie=REAL configuration=VERSIONED original=READ_ONLY audit=FILTERED protected=UNCHANGED workers=NOT_STARTED")
}
