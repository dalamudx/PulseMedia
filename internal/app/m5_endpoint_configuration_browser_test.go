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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
)

func TestM5EndpointConfigurationStatesActualChromeHTTPPG(t *testing.T) {
	runM5EndpointChrome(t, "states")
}
func TestM5EndpointConfigurationRecoveryActualChromeHTTPPG(t *testing.T) {
	runM5EndpointChrome(t, "recovery")
}

type m5EndpointSnapshot struct {
	name, url string
	cipher    []byte
	version   time.Time
}

func runM5EndpointChrome(t *testing.T, mode string) {
	if os.Getenv("PULSE_M5_ENDPOINT_BROWSER") != "1" {
		t.Skip("explicit owned endpoint browser fixture required")
	}
	assets, shots := os.Getenv("PULSE_M5_ENDPOINT_ASSETS"), os.Getenv("PULSE_M5_ENDPOINT_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), "/data/temp/") {
			t.Fatal("owned /data/temp paths required")
		}
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f := organizationSetup(t)
	ctx := t.Context()
	var pgVersion int
	if err := f.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&pgVersion); err != nil || pgVersion != 180006 {
		t.Fatal("PostgreSQL 18.6 required")
	}
	key := bytes.Repeat([]byte{7}, 32)
	cipher, err := crypto.Encrypt(key, []byte("m5-secret-one"))
	if err != nil {
		t.Fatal(err)
	}
	// Repair the inherited download fixture with real encryption. Its hidden URL is
	// deliberately retained byte-for-byte through the safe edit projection.
	raw := "http://m5-hidden-user:m5-hidden-pass@127.0.0.1:1/base?api_key=m5-hidden-query&category=movie"
	if _, err = f.pool.Exec(ctx, `UPDATE download_client SET name='m5-hidden',url=$2,password_encrypted=$3 WHERE id=$1`, f.client, raw, cipher); err != nil {
		t.Fatal(err)
	}
	legacyID := uuid.New()
	if _, err = f.pool.Exec(ctx, `INSERT INTO media_server(id,name,server_type,url,api_key_encrypted,path_mapping) VALUES($1,'m5-legacy','plex','http://127.0.0.1:1',$2,'{}')`, legacyID, cipher); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	hits := map[string]int{}
	failed := false
	badNative := 0
	nativeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 3 {
			badNative++
			http.Error(w, "unexpected native path", http.StatusBadRequest)
			return
		}
		protocol, revision := parts[0], parts[1]
		secret, user := "m5-secret-"+revision, "m5-user-"+revision
		suffix := "/" + strings.Join(parts[2:], "/")
		valid := false
		switch protocol {
		case "qbittorrent":
			valid = suffix == "/api/v2/auth/login" && r.Method == "POST"
			_ = r.ParseForm()
			valid = valid && r.Form.Get("username") == user && r.Form.Get("password") == secret
		case "transmission":
			u, p, ok := r.BasicAuth()
			var rpc struct {
				Method    string `json:"method"`
				Arguments struct {
					Fields []string `json:"fields"`
				} `json:"arguments"`
			}
			err := json.NewDecoder(r.Body).Decode(&rpc)
			valid = suffix == "/transmission/rpc" && r.Method == "POST" && ok && u == user && p == secret && err == nil && rpc.Method == "session-get" && len(rpc.Arguments.Fields) == 1 && rpc.Arguments.Fields[0] == "version"
		case "jellyfin":
			valid = suffix == "/System/Info" && r.Method == "GET" && r.Header.Get("Authorization") == "MediaBrowser Token="+strconv.Quote(secret)
		case "emby":
			valid = suffix == "/System/Info" && r.Method == "GET" && r.Header.Get("X-Emby-Token") == secret
		}
		if !valid {
			badNative++
			http.Error(w, "native credential or protocol mismatch", http.StatusUnauthorized)
			return
		}
		if failed {
			http.Error(w, "owned native offline", http.StatusServiceUnavailable)
			return
		}
		if protocol == "transmission" && r.Header.Get("X-Transmission-Session-Id") != "m5-owned-session" {
			w.Header().Set("X-Transmission-Session-Id", "m5-owned-session")
			w.WriteHeader(http.StatusConflict)
			return
		}
		hits[protocol+"/"+revision]++
		if protocol == "qbittorrent" {
			_, _ = io.WriteString(w, "Ok.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if protocol == "transmission" {
			_, _ = io.WriteString(w, `{"result":"success","arguments":{"version":"4.0"},"tag":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"ServerName":"owned-m5","Version":"1.0"}`)
	})
	native := httptest.NewServer(nativeHandler)
	defer native.Close()
	nativeTwo := httptest.NewServer(nativeHandler)
	defer nativeTwo.Close()

	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	if _, _, err := authService.SetupAdmin(ctx, "revision-owner", "RevisionFixtureOwned123!"); err != nil {
		t.Fatal(err)
	}
	_, session, err := authService.Login(ctx, "revision-owner", "RevisionFixtureOwned123!")
	if err != nil {
		t.Fatal(err)
	}
	manager := download.NewManager(downloadstore.NewRepository(f.pool, f.gen, key))
	manager.SetClientFactory(NativeDownloadClient)
	syncService := mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	syncService.SetCredentialDecoder(crypto.NewSecretProtector(key))
	syncService.SetServerFactory(NativeMediaServerClient)
	settingsService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(key))
	handler := httpapi.NewServerHandler(application, authService, settingsService, nil, nil, nil, nil, nil, nil, nil, manager, syncService, key)
	router := httpapi.NewRouter(application, handler, authService, nil)
	files := http.FileServer(http.Dir(assets))
	snapshots := map[string]m5EndpointSnapshot{}
	var controls sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__m5/control" {
			// This control surface exists only on the task-owned httptest server.
			cookie, e := r.Cookie("pulsemedia_session")
			if r.Method != "POST" || e != nil || cookie.Value != session {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			controls.Lock()
			defer controls.Unlock()
			var input struct{ Action, Family, ID, Revision, Protocol string }
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if input.Action == "native-fail" || input.Action == "native-ok" {
				mu.Lock()
				failed = input.Action == "native-fail"
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if input.Action == "native-verified" {
				mu.Lock()
				valid := badNative == 0 && hits[input.Protocol+"/one"] == 1 && hits[input.Protocol+"/two"] == 1
				mu.Unlock()
				if !valid {
					http.Error(w, "native address/credential counts incorrect", http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			table, column := "download_client", "password_encrypted"
			if input.Family == "media_server" {
				table, column = "media_server", "api_key_encrypted"
			} else if input.Family != "download_client" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			id, e := uuid.Parse(input.ID)
			if e != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			k := input.Family + input.ID
			load := func() (m5EndpointSnapshot, error) {
				var s m5EndpointSnapshot
				e := f.pool.QueryRow(r.Context(), "SELECT name,url,"+column+",updated_at FROM "+table+" WHERE id=$1", id).Scan(&s.name, &s.url, &s.cipher, &s.version)
				return s, e
			}
			var controlErr error
			switch input.Action {
			case "snapshot":
				var s m5EndpointSnapshot
				s, controlErr = load()
				if controlErr == nil {
					snapshots[k] = s
				}
			case "retained", "changed":
				s, e := load()
				before, ok := snapshots[k]
				plain, decryptErr := crypto.Decrypt(key, s.cipher)
				valid := e == nil && ok && decryptErr == nil && s.version.After(before.version)
				if input.Action == "retained" {
					valid = valid && s.url == before.url && bytes.Equal(s.cipher, before.cipher)
				} else {
					valid = valid && s.url == nativeTwo.URL+"/"+input.Protocol+"/two" && string(plain) == "m5-secret-two" && !bytes.Equal(s.cipher, before.cipher)
				}
				if !valid {
					controlErr = fmt.Errorf("persisted address, ciphertext or version assertion failed")
				}
			case "conflict":
				_, controlErr = f.pool.Exec(r.Context(), "UPDATE "+table+" SET name='m5-concurrent',updated_at=clock_timestamp() WHERE id=$1", id)
			case "corrupt":
				_, controlErr = f.pool.Exec(r.Context(), "UPDATE "+table+" SET "+column+"=$2 WHERE id=$1", id, []byte("deliberately-invalid-owned-cipher"))
			case "restore":
				before, ok := snapshots[k]
				if !ok {
					controlErr = fmt.Errorf("missing snapshot")
				} else {
					_, controlErr = f.pool.Exec(r.Context(), "UPDATE "+table+" SET "+column+"=$2 WHERE id=$1", id, before.cipher)
				}
			case "deleted":
				var count int
				controlErr = f.pool.QueryRow(r.Context(), "SELECT count(*) FROM "+table+" WHERE id=$1", id).Scan(&count)
				if count != 0 {
					controlErr = fmt.Errorf("deleted endpoint persisted")
				}
			default:
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if controlErr != nil {
				http.Error(w, "owned DB assertion failed: "+input.Action, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
	browserCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	command := exec.CommandContext(browserCtx, "node", "tests/m5-endpoint-configuration-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M5_ENDPOINT_BASE="+server.URL, "PULSE_M5_ENDPOINT_SESSION="+session, "PULSE_M5_ENDPOINT_CDP="+cdp, "PULSE_M5_ENDPOINT_NATIVE="+native.URL, "PULSE_M5_ENDPOINT_NATIVE_TWO="+nativeTwo.URL, "PULSE_M5_ENDPOINT_MODE="+mode)
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if err := command.Run(); err != nil {
		t.Fatalf("actual endpoint Chrome child failed: %v\n%s", err, output.String())
	}
	receipt := "M5_ENDPOINT_" + strings.ToUpper(mode) + "_PASS"
	if !strings.Contains(output.String(), receipt) {
		t.Fatal("missing actual Chrome receipt")
	}
	mu.Lock()
	invalid := badNative
	mu.Unlock()
	if invalid != 0 {
		t.Fatal("unexpected native request or credentials")
	}
	t.Logf("%s PostgreSQL=%d real_App=true native_factories=true", receipt, pgVersion)
}
