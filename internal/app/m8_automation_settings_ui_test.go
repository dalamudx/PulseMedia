package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
)

// Configuration HTTP is assembled through App. App.Run is deliberately not started:
// saving settings must not be confused with executing sync or attendance.
func TestM8AutomationSettingsUI(t *testing.T) {
	if os.Getenv("PULSE_M8_AUTOMATION_UI") != "1" {
		t.Skip("use task test-m8-automation-settings-ui")
	}
	repo, assets, shots := os.Getenv("PULSE_M8_AUTOMATION_UI_REPO"), os.Getenv("PULSE_M8_AUTOMATION_UI_ASSETS"), os.Getenv("PULSE_M8_AUTOMATION_UI_SHOTS")
	for _, p := range []string{assets, shots, os.TempDir()} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/data/temp/") {
			t.Fatal("owned temporary path required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repository required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal("isolated frontend build required")
	}
	f := m8NewStatsConfigFixture(t)
	key := bytes.Repeat([]byte{7}, 32)
	secrets := crypto.NewSecretProtector(key)
	actor, err := f.auth.AuthenticateToken(t.Context(), f.cookie.Value)
	if err != nil {
		t.Fatal("owned admin authentication failed")
	}
	client, err := f.app.EndpointConfigurationService(key, nil, nil).Create(t.Context(), actor, ports.DownloadClientEndpoint, ports.EndpointConfigurationInput{Name: "Owned automation downloader", Protocol: "qbittorrent", URL: "http://127.0.0.1:1", Mapping: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal("owned downloader configuration setup failed", err)
	}
	movie, tv := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,health,baseline_completed) VALUES($1,$2,'movie','Owned movie root','online',true),($3,$4,'tv','Owned television root','online',true)`, movie, t.TempDir(), tv, t.TempDir())
	other := uuid.New()
	f.exec(t, `INSERT INTO app_user SELECT (jsonb_populate_record(NULL::app_user,to_jsonb(u)||jsonb_build_object('id',$1::text,'username','m8-automation-other','role','admin'))).* FROM app_user u WHERE id=$2`, other, f.admin)
	otherCookie := f.login(t, "m8-automation-other")
	douban := watchlist.NewConfigService(postgres.NewDoubanSyncConfigStore(f.pool, f.app.IDGen()))
	h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, key)
	h.SetDoubanSyncConfigService(douban)
	h.SetRootSelectionService(subscription.NewRootSelectionService(postgres.NewLibraryRootSelectionStore(f.pool)))
	h.SetSiteCheckinConfigService(site.NewCheckinConfigService(postgres.NewSiteCheckinConfigStore(f.pool, f.app.IDGen(), secrets)))
	h.SetSiteCheckinHistoryService(site.NewCheckinHistoryReader(postgres.NewSiteCheckinHistoryStore(f.pool)))
	h.SetIndexerManagementServices(f.manager, search.NewIndexerManagementReceiptService(postgres.NewIndexerManagementStore(f.pool, f.app.IDGen(), secrets)))
	f.router = httpapi.NewRouter(f.app, h, f.auth, nil)
	for _, path := range []string{"/api/v1/library-roots", "/api/v1/indexers", "/api/v1/download-clients"} {
		w := f.request(t.Context(), http.MethodGet, path, "", "", f.cookie, "")
		if w.Code != http.StatusOK {
			t.Fatalf("owned selector preflight path=%s status=%d", path, w.Code)
		}
	}
	protected := m8AutomationProtected(t, f.m7ScrapeFixture)
	var protectionMu sync.Mutex
	var mutations, unexpected atomic.Int32
	var blocked atomic.Bool
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			allowed := r.URL.Path == m8ConfigPath || r.URL.Path == "/api/v1/indexers/"+f.source.ID.String()+"/checkin/config"
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				if r.Method != http.MethodPut || !allowed {
					unexpected.Add(1)
				} else {
					mutations.Add(1)
				}
			}
			f.router.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/owned/") {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			explicitBusinessChange := r.URL.Path == "/owned/source-change" || r.URL.Path == "/owned/demote"
			if explicitBusinessChange {
				protectionMu.Lock()
				defer protectionMu.Unlock()
				if protected != m8AutomationProtected(t, f.m7ScrapeFixture) {
					t.Error("business graph changed before explicit fixture action")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
			var e error
			switch r.URL.Path {
			case "/owned/login/other":
				http.SetCookie(w, otherCookie)
			case "/owned/login/original":
				http.SetCookie(w, f.cookie)
			case "/owned/douban-db/down":
				_, e = f.pool.Exec(r.Context(), `ALTER TABLE douban_sync_config RENAME TO owned_automation_config_offline`)
				if e == nil {
					blocked.Store(true)
				}
			case "/owned/douban-db/up":
				_, e = f.pool.Exec(r.Context(), `ALTER TABLE owned_automation_config_offline RENAME TO douban_sync_config`)
				if e == nil {
					blocked.Store(false)
				}
			case "/owned/douban-conflict":
				var c *ports.DoubanSyncConfig
				c, e = douban.Read(r.Context(), actor)
				if e == nil && c != nil {
					_, e = douban.Save(r.Context(), actor, ports.DoubanSyncInput{DoubanUID: c.DoubanUID, Enabled: !c.Enabled, MovieRootID: c.MovieRootID, TVRootID: c.TVRootID, Bindings: c.Bindings, ExpectedVersion: c.Version}, fmt.Sprintf("owned-automation-conflict-%d", c.Version))
				} else if e == nil {
					e = fmt.Errorf("configuration required")
				}
			case "/owned/source-change":
				changed, changeErr := f.manager.Update(r.Context(), actor, f.source.ID, ports.IndexerManagementInput{Name: "Owned account statistics source updated", Protocol: ports.IndexerProtocolNexusPHP, Categories: []int{}, ExpectedVersion: f.source.ConfigVersion}, "owned-automation-source-change")
				e = changeErr
				if e == nil && changed.Indexer.ConfigVersion <= f.source.ConfigVersion {
					e = fmt.Errorf("source version did not advance")
				}
			case "/owned/demote":
				_, e = f.pool.Exec(r.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, f.admin)
			default:
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if e != nil {
				t.Error("owned browser setup operation failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if explicitBusinessChange {
				protected = m8AutomationProtected(t, f.m7ScrapeFixture)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		if blocked.Load() {
			if _, e := f.pool.Exec(context.Background(), `ALTER TABLE owned_automation_config_offline RENAME TO douban_sync_config`); e != nil {
				t.Error("owned schema restoration failed")
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/m8_automation_settings_ui.mjs"))
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	setup, e := json.Marshal(map[string]any{"base": server.URL, "cookie": f.cookie.Value, "owner": f.admin, "other": other, "sourceID": f.source.ID, "sourceVersion": f.source.ConfigVersion, "movie": movie, "tv": tv, "downloader": client.ID})
	if e != nil {
		t.Fatal("owned setup serialization failed")
	}
	cmd.Env = append(os.Environ(), "PULSE_M8_AUTOMATION_UI_SETUP="+string(setup))
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("automation browser failed (%v); safe diagnostic: %s", runErr, output)
	}
	for _, marker := range []string{"M8_AUTOMATION_UI_BROWSER_PASS", "M8_AUTOMATION_UI_CHROME_CLEANUP_VERIFIED"} {
		if strings.Count(string(output), marker) != 1 {
			t.Fatal("missing browser completion receipt")
		}
	}
	if f.calls.Load() != 0 || unexpected.Load() != 0 || mutations.Load() == 0 {
		t.Fatal("settings made unexpected writes or upstream calls")
	}
	if protected != m8AutomationProtected(t, f.m7ScrapeFixture) {
		t.Fatal("configuration UI changed protected business tables")
	}
	var configs, receipts, checkins, checkinReceipts int
	if e = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM douban_sync_config),(SELECT count(*) FROM douban_sync_config_receipt),(SELECT count(*) FROM site_checkin_config),(SELECT count(*) FROM site_checkin_config_receipt)`).Scan(&configs, &receipts, &checkins, &checkinReceipts); e != nil || configs != 1 || receipts < 2 || checkins != 1 || checkinReceipts < 2 {
		t.Fatal("missing real configuration transactions")
	}
	t.Log(strings.TrimSpace(string(output)))
	t.Logf("M8_AUTOMATION_UI_APP_PASS Cookie=REAL App=REAL PG=18.6 configuration_PUT=%d upstream_calls=0 protected_public_tables=UNCHANGED workers=NOT_STARTED", mutations.Load())
}

func m8AutomationProtected(t *testing.T, f *m7ScrapeFixture) string {
	t.Helper()
	// Only configuration transactions and explicit fixture identity/source edits
	// are allowed to differ. Every other public table is compared in full.
	rows, e := f.pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('app_session','audit_log','outbox_event','douban_sync_config','douban_sync_config_receipt','site_checkin_config','site_checkin_config_receipt') ORDER BY tablename`)
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var table string
		if e = rows.Scan(&table); e != nil {
			rows.Close()
			t.Fatal(e)
		}
		tables = append(tables, table)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		t.Fatal(e)
	}
	return m8ConfigSnapshot(t, f, tables...)
}
