package app

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
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
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	transferstore "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
)

func TestLibraryReuseBrowser(t *testing.T) {
	if os.Getenv("PULSE_REUSE_BROWSER") != "1" {
		t.Skip("explicit owned browser fixture required")
	}
	assets := os.Getenv("PULSE_REUSE_ASSET_DIR")
	if !filepath.IsAbs(assets) || !strings.HasPrefix(assets, "/data/temp/") {
		t.Fatal("owned asset directory required")
	}
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f := organizationSetup(t)
			ctx := t.Context()
			authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
			who, session, e := authService.SetupAdmin(ctx, "reuse-owner", "ReuseFixtureOwned123!")
			if e != nil {
				t.Fatal(e)
			}
			profile, _ := json.Marshal(quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion})
			if _, e = f.pool.Exec(ctx, "UPDATE release_candidate SET quality_profile=$1", profile); e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE download_requirement SET actor_id=$1", who.ID.String()); e != nil {
				t.Fatal(e)
			}
			f.observe(t)
			if e = f.svc.OrganizeDownload(ctx, f.claim(t, "browser-source")); e != nil {
				t.Fatal(e)
			}
			var source uuid.UUID
			if e = f.pool.QueryRow(ctx, "SELECT file_id FROM library_file_unit WHERE unit_id=$1 AND retired_at IS NULL", f.unit).Scan(&source); e != nil {
				t.Fatal(e)
			}
			targetRoot, targetPath := uuid.New(), t.TempDir()
			if e = os.WriteFile(filepath.Join(targetPath, ".pulsemedia_root"), []byte("target"), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: targetRoot, Path: targetPath, Name: "复用目标库", MediaType: kind}); e != nil {
				t.Fatal(e)
			}
			units := []uuid.UUID{f.unit}
			if kind == "tv" {
				if _, e = f.pool.Exec(ctx, "UPDATE media SET media_type='tv',title='Reuse Series' WHERE id=$1", f.media); e != nil {
					t.Fatal(e)
				}
				if _, e = f.pool.Exec(ctx, "UPDATE media_unit SET unit_key='S01E01' WHERE id=$1", f.unit); e != nil {
					t.Fatal(e)
				}
				if _, e = f.pool.Exec(ctx, "UPDATE library_root SET media_type='tv' WHERE id=$1", f.root); e != nil {
					t.Fatal(e)
				}
				extra := uuid.New()
				units = append(units, extra)
				if _, e = f.q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: extra, MediaID: f.media, UnitKey: "S01E02"}); e != nil {
					t.Fatal(e)
				}
				if e = f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: source, UnitID: extra}); e != nil {
					t.Fatal(e)
				}
				if _, e = f.pool.Exec(ctx, "INSERT INTO unit_fact(id,unit_id,stage,source_file_id,root_id,quality_profile) VALUES($1,$2,'imported',$3,$4,$5)", uuid.New(), extra, source, f.root, profile); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = f.pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true,health='online' WHERE id=ANY($1::uuid[])", []uuid.UUID{f.root, targetRoot}); e != nil {
				t.Fatal(e)
			}
			sub := uuid.New()
			if _, e = f.q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: sub, UserID: who.ID, MediaID: f.media, RootID: targetRoot, QualityPolicyID: "default", QualityPolicyRevision: 1, IndexerIds: []byte("[]"), ScopeType: "all"}); e != nil {
				t.Fatal(e)
			}
			for _, id := range units {
				if _, e = f.pool.Exec(ctx, "INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version) VALUES($1,$2,$3,1)", uuid.New(), sub, id); e != nil {
					t.Fatal(e)
				}
			}
			cfg := config.NewDefault()
			cfg.DataDir = t.TempDir()
			application, e := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = application.Close() })
			local := transfer.NewLibraryReuseService(transferstore.NewRepository(f.pool, f.gen, outbox.New(f.pool, f.gen, clock.New()), rootfs.NewFilesystem(), transfer.ExecutePreparedPlan))
			handler := httpapi.NewServerHandler(application, authService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			handler.SetLibraryReuseService(local)
			handler.SetSubscriptionStatusService(subscription.NewStatusService(postgres.NewSubscriptionStatusStore(f.pool)))
			router := httpapi.NewRouter(application, handler, authService, nil)
			files := http.FileServer(http.Dir(assets))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					router.ServeHTTP(w, r)
				} else if strings.HasPrefix(r.URL.Path, "/assets/") {
					files.ServeHTTP(w, r)
				} else {
					http.ServeFile(w, r, filepath.Join(assets, "index.html"))
				}
			}))
			defer server.Close()
			workerCtx, stop := context.WithCancel(ctx)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); application.runOrganizationWorker(workerCtx, f.queue, f.svc, local) }()
			t.Cleanup(func() { stop(); wg.Wait() })
			browserCtx, cancel := context.WithTimeout(ctx, 100*time.Second)
			defer cancel()
			cdp := startOwnedRevisionChrome(t, browserCtx)
			cmd := exec.CommandContext(browserCtx, "node", "tests/library-reuse-browser.mjs")
			cmd.Dir = "../../web"
			cmd.Env = append(os.Environ(), "PULSE_REUSE_UI_BASE="+server.URL, "PULSE_REUSE_UI_SESSION="+session, "PULSE_REUSE_UI_CDP="+cdp, "PULSE_REUSE_UI_KIND="+kind)
			raw, e := cmd.CombinedOutput()
			if e != nil {
				t.Fatalf("reuse browser %v %s", e, raw)
			}
			if !strings.Contains(string(raw), "LIBRARY_REUSE_BROWSER_PASSED") {
				t.Fatal(string(raw))
			}
			var plans, targets, facts, protected int
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM library_reuse_plan WHERE state='completed'").Scan(&plans); e != nil {
				t.Fatal(e)
			}
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM library_file f JOIN library_file_unit u ON u.file_id=f.id WHERE f.root_id=$1 AND u.retired_at IS NULL", targetRoot).Scan(&targets); e != nil {
				t.Fatal(e)
			}
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE root_id=$1 AND stage='imported' AND revoked_at IS NULL", targetRoot).Scan(&facts); e != nil {
				t.Fatal(e)
			}
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM library_reuse_unit WHERE active").Scan(&protected); e != nil {
				t.Fatal(e)
			}
			if plans != 1 || targets != len(units) || facts != len(units) || protected != 0 || f.count(t, "download_task") != 1 {
				t.Fatalf("wrong lifecycle plans=%d mapping=%d facts=%d protected=%d", plans, targets, facts, protected)
			}
			if _, e = os.Stat(f.path); e != nil {
				t.Fatal("source lost", e)
			}
			t.Log(strings.TrimSpace(string(raw)))
		})
	}
}

var _ ports.LibraryReuseRepository = (*transferstore.Repository)(nil)
