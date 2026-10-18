package app

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
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
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
)

func TestDashboardClassificationAndRefreshBrowser(t *testing.T) {
	if os.Getenv("PULSE_DASHBOARD_BROWSER") != "1" {
		t.Skip("explicit owned browser fixture required")
	}
	assets := os.Getenv("PULSE_DASHBOARD_ASSET_DIR")
	if !filepath.IsAbs(assets) || !strings.HasPrefix(assets, "/data/temp/") {
		t.Fatal("owned absolute asset directory required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f, _, actor, _ := revisionSetup(t)
	ctx := t.Context()
	owner := uuid.MustParse(actor.ID)
	tvRoot := uuid.New()
	if _, err := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: tvRoot, Path: t.TempDir(), MediaType: "tv", Name: "Dashboard TV"}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ kind, scope string }{{"movie", "all"}, {"tv", "all"}, {"tv", "selected"}} {
		mediaID, rootID := f.media, f.root
		if item.kind == "tv" {
			mediaID, rootID = uuid.New(), tvRoot
			if _, err := f.pool.Exec(ctx, "INSERT INTO media(id,title,media_type) VALUES($1,$2,'tv')", mediaID, "Dashboard "+item.scope); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: uuid.New(), UserID: owner, MediaID: mediaID, RootID: rootID, QualityPolicyID: "default", QualityPolicyRevision: 1, IndexerIds: []byte("[]"), ScopeType: item.scope}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	_, session, err := authService.Login(ctx, "revision-owner", "RevisionFixtureOwned123!")
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewServerHandler(application, authService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := httpapi.NewRouter(application, handler, authService, nil)
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			router.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	defer server.Close()
	for _, role := range []string{"admin", "member"} {
		t.Run(role, func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, "UPDATE app_user SET role=$2 WHERE id=$1", owner, role); err != nil {
				t.Fatal(err)
			}
			browserCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
			defer cancel()
			cdp := startOwnedRevisionChrome(t, browserCtx)
			command := exec.CommandContext(browserCtx, "node", "tests/dashboard-browser.mjs")
			command.Dir = "../../web"
			command.Env = append(os.Environ(), "PULSE_DASHBOARD_UI_BASE="+server.URL, "PULSE_DASHBOARD_UI_SESSION="+session, "PULSE_DASHBOARD_UI_CDP="+cdp, "PULSE_DASHBOARD_UI_ROLE="+role)
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("real dashboard browser: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "DASHBOARD_BROWSER_PASSED") {
				t.Fatalf("missing browser acceptance: %s", out)
			}
			t.Log(strings.TrimSpace(string(out)))
		})
	}
}
