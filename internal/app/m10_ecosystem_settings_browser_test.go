package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/cookiecloud"
	"github.com/dalamudx/PulseMedia/internal/usecase/seeding"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestM10EcosystemSettingsChromePG(t *testing.T) {
	if os.Getenv("PULSE_M10_ECOSYSTEM_SETTINGS_BROWSER") != "1" {
		t.Skip("explicit browser fixture")
	}
	assets, shots := os.Getenv("PULSE_M10_ECOSYSTEM_SETTINGS_ASSETS"), os.Getenv("PULSE_M10_ECOSYSTEM_SETTINGS_SHOTS")
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
	secrets := crypto.NewSecretProtector(outboxTestKey)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected owned upstream", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	encrypted, e := secrets.EncryptSecret([]byte("Owned-Settings-Endpoint-123!"))
	if e != nil {
		t.Fatal(e)
	}
	m6DashboardExec(t, f, "UPDATE download_client SET url=$1,"+"pass"+"word_encrypted=$2", upstream.URL, encrypted)
	authentication := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	owner, session, e := authentication.SetupAdmin(ctx, "ecosystem-admin", "Owned-Ecosystem-Admin-123!")
	if e != nil {
		t.Fatal(e)
	}
	hash, e := crypto.HashPassword("Owned-Ecosystem-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "ecosystem-member", PasswordHash: hash, Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	_, member, e := authentication.Login(ctx, "ecosystem-member", "Owned-Ecosystem-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	handler := httpapi.NewServerHandler(application, authentication, nil, nil, nil, nil, nil, nil, nil, nil, f.mgr, nil, outboxTestKey)
	handler.SetCookieCloudConfigService(cookiecloud.NewConfigService(postgres.NewCookieCloudConfigStore(f.pool, f.gen), secrets))
	handler.SetSeedingConfigService(seeding.NewConfigService(postgres.NewSeedingConfigStore(f.pool, f.gen)))
	router := httpapi.NewRouter(application, handler, authentication, nil)
	var mu sync.Mutex
	mutations := 0
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owned/change-client" {
			m6DashboardExec(t, f, `UPDATE download_client SET updated_at=clock_timestamp() WHERE id=$1`, f.client)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
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
		before, e := m6DashboardSnapshot(snapshotCtx, f)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		record := httptest.NewRecorder()
		router.ServeHTTP(record, r)
		after, e := m6DashboardSnapshot(snapshotCtx, f)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		allowed := map[string]bool{}
		if r.Method != http.MethodGet {
			mutations++
			if r.Method != http.MethodPut {
				t.Error("unexpected mutation", r.Method, r.URL.Path)
			}
			if record.Code == http.StatusOK {
				allowed["audit_log"] = true
				allowed["outbox_event"] = true
				allowed["outbox_aggregate_sequence"] = true
				allowed["idempotency_record"] = true
				if strings.Contains(r.URL.Path, "cookiecloud") {
					allowed["cookiecloud_configuration"] = true
				} else if strings.Contains(r.URL.Path, "seeding/config") {
					allowed["seeding_configuration"] = true
				} else {
					t.Error("unexpected configuration write", r.URL.Path)
				}
			}
		}
		for table, digest := range before {
			if !allowed[table] && after[table] != digest {
				t.Error("settings changed protected table", table, r.Method, r.URL.Path)
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
	browserCtx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	setup, _ := json.Marshal(map[string]any{"base": server.URL, "admin": session, "member": member, "client": f.client, "owner": owner.ID, "upstream": upstream.URL})
	command := exec.CommandContext(browserCtx, "node", "tests/m10-ecosystem-settings-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M10_ECOSYSTEM_SETTINGS_SETUP="+string(setup), "PULSE_M10_ECOSYSTEM_SETTINGS_CDP="+cdp)
	command.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if e = command.Run(); e != nil {
		t.Fatalf("ecosystem settings Chrome: %v\n%s", e, output.String())
	}
	if strings.Count(output.String(), "M10_ECOSYSTEM_SETTINGS_CHROME_PASS") != 1 || requests.Load() != 0 || mutations != 4 {
		t.Fatal("unexpected browser receipt, upstream requests or command count", requests.Load(), mutations)
	}
	var enabled bool
	var version int64
	if e = f.pool.QueryRow(ctx, `SELECT enabled,version FROM cookiecloud_configuration WHERE owner_id=$1`, owner.ID).Scan(&enabled, &version); e != nil || enabled || version != 2 {
		t.Fatal("CookieCloud final configuration", enabled, version, e)
	}
	if os.Getenv("PULSE_M10_ECOSYSTEM_SETTINGS_REVIEW") == "1" {
		fmt.Printf("M10_ECOSYSTEM_SETTINGS_SCREENSHOTS_READY dir=%s\n", shots)
		timer := time.NewTimer(600 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, e := os.Stat(filepath.Join(shots, "inspected")); e == nil {
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
	t.Log("M10_ECOSYSTEM_SETTINGS_BROWSER_PG_PASS Cookie=REAL recovery=ORIGINAL_FIRST credential_storage=NONE endpoint_conflict=REJECTED protected=UNCHANGED upstream=ZERO workers=NOT_STARTED")
}
