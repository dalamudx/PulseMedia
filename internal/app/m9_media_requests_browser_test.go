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
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

// This fixture exercises the production HTTP/usecase/store stack without starting
// workers. Native Add and physical organization are covered by the separate App
// acceptance; a browser approval is never reported as download success.
func TestM9MediaRequestsChromePG(t *testing.T) {
	if os.Getenv("PULSE_M9_REQUEST_BROWSER") != "1" {
		t.Skip("owned media-request Chrome/PostgreSQL fixture required")
	}
	assets, shots := os.Getenv("PULSE_M9_REQUEST_ASSETS"), os.Getenv("PULSE_M9_REQUEST_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		clean := filepath.Clean(path)
		resolved, err := filepath.EvalSymlinks(clean)
		if !filepath.IsAbs(path) || !strings.HasPrefix(clean, "/data/temp/") || err != nil || resolved != clean {
			t.Fatal("absolute owned /data/temp paths without symlinks required")
		}
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal("isolated frontend build required")
	}
	f := organizationSetup(t)
	ctx := t.Context()
	var version string
	if err := f.pool.QueryRow(ctx, `SHOW server_version_num`).Scan(&version); err != nil || version != "180006" {
		t.Fatal("private PostgreSQL 18.6 required")
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	protector := crypto.NewSecretProtector(outboxTestKey)
	var unexpected, metadataCalls atomic.Int32
	// Every configured source is owned. Even a seemingly harmless source GET is
	// unexpected: reads and approval do not authorize a downloader/indexer call.
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unexpected.Add(1)
		http.Error(w, "unexpected owned protocol call", http.StatusServiceUnavailable)
	}))
	t.Cleanup(source.Close)
	password, err := protector.EncryptSecret([]byte("Owned-Request-Endpoint-123!"))
	if err != nil {
		t.Fatal(err)
	}
	m6DashboardExec(t, f, `UPDATE download_client SET url=$1,password_encrypted=$2`, source.URL, password)
	m6DashboardExec(t, f, `UPDATE indexer SET url=$1`, source.URL)
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, f.pool, protector, usecaseSearch.ClassifyLegacyIndexerProtocol); err != nil {
		t.Fatal("seal owned source configuration", err)
	}
	var indexer uuid.UUID
	if err = f.pool.QueryRow(ctx, `SELECT id FROM indexer ORDER BY id LIMIT 1`).Scan(&indexer); err != nil {
		t.Fatal(err)
	}
	tvRoot, tvPath := uuid.New(), t.TempDir()
	if err = os.WriteFile(filepath.Join(tvPath, ".pulsemedia_root"), []byte("owned requests television root"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: tvRoot, Path: tvPath, MediaType: "tv", Name: "家庭剧集目标根-" + strings.Repeat("非常长的名称用于验证窄屏换行", 12)}); err != nil {
		t.Fatal(err)
	}
	if _, err = library.NewScanner(f.gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.pool)).ScanRoot(ctx, tvRoot); err != nil {
		t.Fatal("real television baseline scan", err)
	}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("language") != "zh-CN" {
			unexpected.Add(1)
			http.Error(w, "unexpected metadata request", http.StatusBadRequest)
			return
		}
		var body string
		switch r.URL.Path {
		case "/3/tv/1399":
			body = `{"id":1399,"name":"Owned sparse television","original_name":"Owned sparse television","first_air_date":"2011-04-17","overview":"Owned native synopsis","seasons":[{"season_number":1,"episode_count":2,"name":"First season","air_date":"2011-04-17"}],"number_of_seasons":1,"number_of_episodes":2}`
		case "/3/tv/1399/season/1":
			body = `{"season_number":1,"episodes":[{"id":13990102,"season_number":1,"episode_number":2,"name":"Second episode","air_date":"2011-04-24"},{"id":13990107,"season_number":1,"episode_number":7,"name":"Seventh episode","air_date":null}]}`
		case "/3/movie/550":
			body = `{"id":550,"title":"Owned native movie","original_title":"Owned native movie","release_date":"1999-10-15","overview":"Owned native movie synopsis"}`
		default:
			unexpected.Add(1)
			http.NotFound(w, r)
			return
		}
		metadataCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(metadata.Close)
	provider := tmdb.NewClient("owned-request-metadata", tmdb.WithBaseURL(metadata.URL+"/3"), tmdb.WithHTTPClient(metadata.Client()))
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	admin, adminSession, err := authService.SetupAdmin(ctx, "requests-admin", "Owned-Request-Admin-123!")
	if err != nil {
		t.Fatal("owned administrator setup", err)
	}
	memberA, memberB := uuid.New(), uuid.New()
	hash, err := crypto.HashPassword("Owned-Request-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct {
		id   uuid.UUID
		name string
	}{{memberA, "requests-member-a"}, {memberB, "requests-member-b"}} {
		m6DashboardExec(t, f, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,$2,$3,'member')`, user.id, user.name, hash)
	}
	_, sessionA, err := authService.Login(ctx, "requests-member-a", "Owned-Request-Member-123!")
	if err != nil {
		t.Fatal("owned member A login", err)
	}
	_, sessionB, err := authService.Login(ctx, "requests-member-b", "Owned-Request-Member-123!")
	if err != nil {
		t.Fatal("owned member B login", err)
	}
	settingService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(outboxTestKey))
	syncService := mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	syncService.SetCredentialDecoder(protector)
	syncService.SetServerFactory(NativeMediaServerClient)
	f.mgr.SetClientFactory(NativeDownloadClient)
	h := httpapi.NewServerHandler(application, authService, settingService, nil, nil, provider, nil, policy.NewService(postgres.NewPolicyStore(f.pool, f.gen)), nil, nil, f.mgr, syncService, outboxTestKey)
	h.SetRootSelectionService(subscription.NewRootSelectionService(postgres.NewLibraryRootSelectionStore(f.pool)))
	indexerStore := postgres.NewIndexerManagementStore(f.pool, f.gen, protector)
	h.SetIndexerManagementServices(usecaseSearch.NewIndexerManagementService(indexerStore, protector), usecaseSearch.NewIndexerManagementReceiptService(indexerStore))
	h.SetMediaRequestService(subscription.NewMediaRequestService(postgres.NewMediaRequestStore(f.pool, f.gen, clock.New()), func(fence ports.MediaRequestApprovalFence) ports.SubscriptionCreator {
		return subscription.NewSubscriptionCreationService(postgres.NewMediaRequestApprovalCreationStore(f.pool, f.gen, fence), provider, provider, protector)
	}))
	router := httpapi.NewRouter(application, h, authService, nil)
	baseline, err := m6DashboardSnapshot(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// Counts complement full-row hashes: permitted writes must have exact effects,
	// while every table outside the command's allowlist remains byte-for-byte equal.
	ledgerTables := []string{"media_request", "subscription", "wanted_item", "audit_log", "outbox_event", "idempotency_record", "media", "media_identity", "media_unit", "media_unit_identity", "tv_season_metadata_snapshot"}
	counts := func(ctx context.Context) (map[string]int64, error) {
		out := make(map[string]int64, len(ledgerTables))
		for _, table := range ledgerTables {
			var n int64
			if e := f.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil {
				return nil, e
			}
			out[table] = n
		}
		return out, nil
	}
	initialCounts, err := counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	reads, writes, successes, flips := 0, 0, 0, 0
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__m9_requests/") || strings.HasPrefix(r.URL.Path, "/api/") {
			mu.Lock()
			defer mu.Unlock()
			before, e := m6DashboardSnapshot(r.Context(), f)
			if e != nil || !reflect.DeepEqual(before, baseline) {
				t.Error("full non-authentication rows changed between browser commands")
				http.Error(w, "protected snapshot mismatch", 500)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/__m9_requests/") {
				switch {
				case r.URL.Path == "/__m9_requests/counters" && r.Method == http.MethodGet:
					current, ce := counts(r.Context())
					if ce != nil {
						http.Error(w, "counter failure", 500)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"reads": reads, "writes": writes, "successes": successes, "metadata": metadataCalls.Load(), "unexpected": unexpected.Load(), "counts": current})
				case (r.URL.Path == "/__m9_requests/demote" || r.URL.Path == "/__m9_requests/restore") && r.Method == http.MethodPost:
					role := "member"
					if strings.HasSuffix(r.URL.Path, "/restore") {
						role = "admin"
					}
					if _, e = f.pool.Exec(r.Context(), `UPDATE app_user SET role=$1 WHERE id=$2`, role, admin.ID); e != nil {
						t.Error("owned role transition failed")
						http.Error(w, "role transition failure", 500)
						return
					}
					flips++
					w.WriteHeader(http.StatusNoContent)
				default:
					unexpected.Add(1)
					http.NotFound(w, r)
				}
				return
			}
			metaBefore := metadataCalls.Load()
			oldCounts, ce := counts(r.Context())
			if ce != nil {
				t.Error("pre-command count failure")
				http.Error(w, "count failure", 500)
				return
			}
			recorded := httptest.NewRecorder()
			isWrite := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
			if isWrite {
				writes++
			} else {
				reads++
			}
			router.ServeHTTP(recorded, r)
			after, se := m6DashboardSnapshot(r.Context(), f)
			newCounts, ne := counts(r.Context())
			allowed := map[string]bool{}
			delta := map[string]int64{}
			if isWrite && recorded.Code >= 200 && recorded.Code < 300 {
				successes++
				if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/api/v1/media-requests") {
					t.Error("unexpected successful browser write")
					unexpected.Add(1)
				}
				for _, table := range []string{"media_request", "audit_log", "outbox_event", "idempotency_record"} {
					allowed[table] = true
				}
				delta["audit_log"], delta["outbox_event"], delta["idempotency_record"] = 1, 1, 1
				switch {
				case r.URL.Path == "/api/v1/media-requests":
					delta["media_request"] = 1
				case strings.HasSuffix(r.URL.Path, "/close"):
				case strings.HasSuffix(r.URL.Path, "/approve"):
					for _, table := range []string{"subscription", "wanted_item", "media", "media_identity", "media_unit", "media_unit_identity", "tv_season_metadata_snapshot"} {
						allowed[table] = true
					}
					delta["subscription"], delta["wanted_item"] = 1, 2
					delta["media"], delta["media_identity"] = 1, 1
					delta["media_unit"], delta["media_unit_identity"], delta["tv_season_metadata_snapshot"] = 2, 2, 1
					delta["audit_log"], delta["outbox_event"] = 2, 2
				default:
					t.Error("unexpected request command")
				}
			}
			if se != nil || ne != nil {
				t.Error("post-command snapshot/count failure")
			}
			for table, digest := range before {
				if !allowed[table] && after[table] != digest {
					t.Errorf("browser changed protected table %s", table)
				}
			}
			for _, table := range ledgerTables {
				if newCounts[table]-oldCounts[table] != delta[table] {
					t.Errorf("unexpected command row delta for %s", table)
				}
			}
			if !strings.HasSuffix(r.URL.Path, "/approve") && metadataCalls.Load() != metaBefore {
				t.Error("read or non-approval command contacted metadata")
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
	setup, err := json.Marshal(map[string]any{"base": server.URL, "adminSession": adminSession, "sessionA": sessionA, "sessionB": sessionB, "memberA": memberA, "tvRoot": tvRoot, "indexer": indexer, "downloader": f.client})
	if err != nil {
		t.Fatal("owned browser setup serialization failed")
	}
	command := exec.CommandContext(browserCtx, "node", "tests/m9-media-requests-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M9_REQUEST_SETUP="+string(setup), "PULSE_M9_REQUEST_CDP="+cdp)
	command.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if err = command.Run(); err != nil {
		t.Fatalf("media-request Chrome: %v\n%s", err, output.String())
	}
	if strings.Count(output.String(), "M9_REQUEST_CHROME_PASS") != 1 {
		t.Fatal("missing unique Chrome receipt")
	}
	mu.Lock()
	after, err := m6DashboardSnapshot(ctx, f)
	if err != nil || !reflect.DeepEqual(after, baseline) {
		t.Error("final read/recovery changed non-authentication rows")
	}
	finalCounts, err := counts(ctx)
	if err != nil {
		t.Error("final ledger unavailable")
	}
	for table, expected := range map[string]int64{"media_request": 3, "subscription": 1, "wanted_item": 2, "audit_log": 7, "outbox_event": 7, "idempotency_record": 6} {
		if finalCounts[table]-initialCounts[table] != expected {
			t.Errorf("final %s delta mismatch", table)
		}
	}
	if writes != 7 || successes != 6 || flips != 2 || reads == 0 || metadataCalls.Load() != 2 || unexpected.Load() != 0 {
		t.Error("unexpected browser command/native-call ledger", writes, successes, flips, reads, metadataCalls.Load(), unexpected.Load())
	}
	mu.Unlock()
	var approved, withdrawn, rejected, sparse int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='approved' AND version=2),count(*) FILTER(WHERE state='withdrawn' AND version=2),count(*) FILTER(WHERE state='rejected' AND version=2) FROM media_request WHERE owner_id=$1`, memberA).Scan(&approved, &withdrawn, &rejected); err != nil || approved != 1 || withdrawn != 1 || rejected != 1 {
		t.Error("missing real terminal request transitions")
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id JOIN subscription s ON s.id=w.subscription_id WHERE s.user_id=$1 AND s.root_id=$2 AND u.season_number=1 AND u.episode_number IN (2,7)`, memberA, tvRoot).Scan(&sparse); err != nil || sparse != 2 {
		t.Error("approval did not preserve native sparse episodes 2 and 7")
	}
	var knownDate, unknownDate int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE u.episode_number=2 AND u.air_date=DATE '2011-04-24'),count(*) FILTER(WHERE u.episode_number=7 AND u.air_date IS NULL) FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id JOIN subscription s ON s.id=w.subscription_id WHERE s.user_id=$1 AND s.root_id=$2`, memberA, tvRoot).Scan(&knownDate, &unknownDate); err != nil || knownDate != 1 || unknownDate != 1 {
		t.Error("approval invented or lost native air dates")
	}
	if os.Getenv("PULSE_M9_REQUEST_REVIEW") == "1" {
		fmt.Printf("M9_REQUEST_UI_SCREENSHOTS_READY dir=%s\n", shots)
		deadline := time.NewTimer(150 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if info, e := os.Stat(filepath.Join(shots, "inspected")); e == nil && info.Mode().IsRegular() {
				break
			}
			select {
			case <-deadline.C:
				t.Fatal("screenshot inspection receipt missing")
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("screenshot inspection cancelled")
			}
		}
	}
	t.Log("M9_REQUEST_NATIVE_CHROME_APP_PG_PASS Cookie=REAL approved=1 sparse_Wanted=2 successful_commands=6 conflict_commands=1 native_metadata=2 source_calls=0 snapshots=FULL_NON_AUTH_TABLES viewports=4 screenshots=6 workers=NOT_STARTED")
}
