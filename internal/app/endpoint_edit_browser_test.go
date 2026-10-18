package app

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	searchcase "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
)

func TestEndpointURLSafeEditBrowser(t *testing.T) {
	if os.Getenv("PULSE_DASHBOARD_BROWSER") != "1" {
		t.Skip("explicit owned browser fixture required")
	}
	assets := os.Getenv("PULSE_DASHBOARD_ASSET_DIR")
	if !filepath.IsAbs(assets) || !strings.HasPrefix(assets, "/data/temp/") {
		t.Fatal("owned asset directory required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f, _, _, _ := revisionSetup(t)
	ctx := t.Context()
	raw := "http://legacy-user:fixture-user-secret@127.0.0.1:1/base?api_key=fixture-query-secret&category=movie"
	targets := []struct {
		Table string `json:"table"`
		Path  string `json:"path"`
		Name  string `json:"name"`
		ID    string `json:"id"`
		SQL   string `json:"-"`
	}{
		{"indexer", "indexers", "m4-safe-indexer", uuid.NewString(), `INSERT INTO indexer(id,name,url,api_key_encrypted,categories) VALUES($1,$2,$3,'fixture-cipher','[]')`},
		{"download_client", "download-clients", "m4-safe-client", uuid.NewString(), `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,$2,'qbittorrent',$3,'fixture-user','fixture-cipher','[]')`},
		{"media_server", "media-servers", "m4-safe-server", uuid.NewString(), `INSERT INTO media_server(id,name,server_type,url,api_key_encrypted,path_mapping) VALUES($1,$2,'jellyfin',$3,'fixture-cipher','{}')`},
	}
	for _, target := range targets {
		if _, err := f.pool.Exec(ctx, target.SQL, uuid.MustParse(target.ID), target.Name, raw); err != nil {
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
	key := bytes.Repeat([]byte{7}, 32)
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	_, session, err := authService.Login(ctx, "revision-owner", "RevisionFixtureOwned123!")
	if err != nil {
		t.Fatal(err)
	}
	searchService := searchcase.NewService(postgres.NewSearchExecutionStore(f.pool), f.gen, crypto.NewSecretProtector(key))
	searchService.SetHealthRepository(postgres.NewIndexerHealthStore(f.pool, f.gen))
	settingsService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(key))
	handler := httpapi.NewServerHandler(application, authService, settingsService, nil, nil, nil, nil, nil, searchService, nil, nil, nil, key)
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
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	encoded, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(browserCtx, "node", "tests/endpoint-edit-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_ENDPOINT_UI_BASE="+server.URL, "PULSE_ENDPOINT_UI_SESSION="+session, "PULSE_ENDPOINT_UI_CDP="+cdp, "PULSE_ENDPOINT_UI_TARGETS="+string(encoded))
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real endpoint edit browser: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ENDPOINT_SAFE_EDIT_BROWSER_PASSED") {
		t.Fatalf("missing endpoint acceptance: %s", out)
	}
	for _, target := range targets {
		var name, persisted string
		var ciphertext []byte
		secretColumn := map[string]string{"indexer": "api_key_encrypted", "download_client": "password_encrypted", "media_server": "api_key_encrypted"}[target.Table]
		// Table and column identifiers come exclusively from the fixed fixtures above.
		if err := f.pool.QueryRow(ctx, "SELECT name,url,"+secretColumn+" FROM "+target.Table+" WHERE id=$1", uuid.MustParse(target.ID)).Scan(&name, &persisted, &ciphertext); err != nil {
			t.Fatal(err)
		}
		if name != target.Name+" edited" || persisted != raw || !bytes.Equal(ciphertext, []byte("fixture-cipher")) {
			t.Fatalf("%s edit changed hidden endpoint/credential or failed to save name", target.Table)
		}
	}
	t.Log(strings.TrimSpace(string(out)))
}
