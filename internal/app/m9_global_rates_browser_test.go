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
	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/downloadmetrics"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func TestM9GlobalRatesNativeChromePG(t *testing.T) { runM9GlobalRatesChrome(t, false) }

func runM9GlobalRatesChrome(t *testing.T, inbox bool) {
	if os.Getenv("PULSE_M9_RATE_BROWSER") != "1" {
		t.Skip("owned native global-rate Chrome fixture required")
	}
	assets, shots := os.Getenv("PULSE_M9_RATE_ASSETS"), os.Getenv("PULSE_M9_RATE_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), "/data/temp/") {
			t.Fatal("owned paths required")
		}
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f := organizationSetup(t)
	ctx := t.Context()
	key := outboxTestKey
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.DownloadGlobalRates.Enabled = true
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	// Browser reads have no competing worker: native observations use the real
	// collector/repository/queue below; full App scheduling is verified separately.
	var calls, unexpected atomic.Int32
	var failed atomic.Bool
	var faster atomic.Bool
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "owned-rate-session", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/transfer/info":
			if r.Method != http.MethodGet {
				unexpected.Add(1)
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			calls.Add(1)
			if failed.Load() {
				http.Error(w, "owned rejection", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			down, up := int64(262144), int64(65536)
			if faster.Load() {
				down, up = 524288, 131072
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"dl_info_speed": down, "up_info_speed": up})
		default:
			unexpected.Add(1)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(qb.Close)
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			unexpected.Add(1)
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "owned-rate-session" {
			w.Header().Set("X-Transmission-Session-Id", "owned-rate-session")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "session-stats" {
			unexpected.Add(1)
			http.Error(w, "method", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		if failed.Load() {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "owned rate limit", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		down := int64(32768)
		if faster.Load() {
			down = 65536
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": map[string]int64{"downloadSpeed": down, "uploadSpeed": 0}})
	}))
	t.Cleanup(tr.Close)
	protector := crypto.NewSecretProtector(key)
	encrypted, err := protector.EncryptSecret([]byte("owned-rate-password"))
	if err != nil {
		t.Fatal(err)
	}
	m6DashboardExec(t, f, `UPDATE download_client SET name=$2,url=$3,password_encrypted=$4 WHERE id=$1`, f.client, "客厅-qBittorrent-"+strings.Repeat("長い名前", 12), qb.URL, encrypted)
	other := uuid.New()
	if _, err = f.q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: other, Name: "书房 Transmission", ClientType: "transmission", Url: tr.URL, Username: "owned-rate-account", PasswordEncrypted: encrypted, RemotePathMap: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewDownloadGlobalRatesStore(f.pool, f.gen, protector)
	collector := downloadmetrics.NewService(repo, downloadGlobalRatesBuilder{}, clock.New())
	collect := func(ctx context.Context) error {
		if _, e := f.pool.Exec(ctx, `UPDATE download_global_rate_schedule SET next_due_at=clock_timestamp()-interval '1 second'`); e != nil {
			return e
		}
		if e := repo.EnqueueDue(ctx); e != nil {
			return e
		}
		for range 2 {
			job, e := f.queue.Claim(ctx, []string{ports.DownloadGlobalRatesJobKind}, "owned-rate-chrome", time.Minute)
			if e != nil {
				return e
			}
			if job == nil {
				return fmt.Errorf("expected native collection job")
			}
			if e = collector.Execute(ctx, ports.JobLease{ID: job.ID, Owner: "owned-rate-chrome", Token: job.LeaseToken}); e != nil {
				return e
			}
		}
		return nil
	}
	if err = collect(ctx); err != nil {
		t.Fatal("initial native samples", err)
	}
	// A second real sample crosses a history grid boundary without changing any
	// stored timestamp, proving the native waveform rather than only a dot.
	wait := time.NewTimer(11 * time.Second)
	select {
	case <-wait.C:
	case <-ctx.Done():
		wait.Stop()
		t.Fatal("native history sample wait cancelled")
	}
	faster.Store(true)
	if err = collect(ctx); err != nil {
		t.Fatal("second native history samples", err)
	}
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	_, adminSession, err := authService.SetupAdmin(ctx, "rates-admin", "Owned-Rate-Admin-123!")
	if err != nil {
		t.Fatal(err)
	}
	member := uuid.New()
	hash, err := crypto.HashPassword("Owned-Rate-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	m6DashboardExec(t, f, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'rates-member',$2,'member')`, member, hash)
	_, memberSession, err := authService.Login(ctx, "rates-member", "Owned-Rate-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(metadata.Close)
	provider := tmdb.NewClient("owned-rate-metadata", tmdb.WithBaseURL(metadata.URL), tmdb.WithHTTPClient(metadata.Client()))
	settingService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(key))
	syncService := mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	if err = postgres.EncryptLegacyIndexerConfigurations(ctx, f.pool, protector, usecaseSearch.ClassifyLegacyIndexerProtocol); err != nil {
		t.Fatal("source configuration initialization", err)
	}
	syncService.SetCredentialDecoder(protector)
	syncService.SetServerFactory(NativeMediaServerClient)
	f.mgr.SetClientFactory(NativeDownloadClient)
	apiHandler := httpapi.NewServerHandler(application, authService, settingService, nil, nil, provider, nil, policy.NewService(postgres.NewPolicyStore(f.pool, f.gen)), nil, nil, f.mgr, syncService, key)
	indexerStore := postgres.NewIndexerManagementStore(f.pool, f.gen, protector)
	apiHandler.SetIndexerManagementServices(usecaseSearch.NewIndexerManagementService(indexerStore, protector), usecaseSearch.NewIndexerManagementReceiptService(indexerStore))
	apiHandler.SetRootSelectionService(subscription.NewRootSelectionService(postgres.NewLibraryRootSelectionStore(f.pool)))
	requestStore := postgres.NewMediaRequestStore(f.pool, f.gen, clock.New())
	apiHandler.SetMediaRequestService(subscription.NewMediaRequestService(requestStore, func(fence ports.MediaRequestApprovalFence) ports.SubscriptionCreator {
		return subscription.NewSubscriptionCreationService(postgres.NewMediaRequestApprovalCreationStore(f.pool, f.gen, fence), provider, provider, protector)
	}))
	var closePath string
	closed := 0
	if inbox {
		owner := &ports.Actor{Type: ports.ActorTypeUser, ID: member.String(), Role: ports.RoleMember}
		for i := range 3 {
			value, e := requestStore.SubmitMediaRequest(ctx, owner, mediarequest.Intent{MediaType: "movie", TMDBID: int64(550 + i), Seasons: []int{}}, fmt.Sprintf("home-inbox-%d", i))
			if e != nil {
				t.Fatal("real pending request", e)
			}
			if i == 0 {
				closePath = "/api/v1/media-requests/" + value.Request.ID.String() + "/close"
			}
		}
	}
	handler := httpapi.NewRouter(application, apiHandler, authService, nil)
	files := http.FileServer(http.Dir(assets))
	baseline, err := m6DashboardSnapshot(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	changes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inbox && strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet {
			if r.Method != http.MethodPost || r.URL.Path != closePath {
				http.Error(w, "unexpected write", http.StatusForbidden)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			current, e := m6DashboardSnapshot(r.Context(), f)
			if e != nil || !reflect.DeepEqual(current, baseline) {
				http.Error(w, "snapshot before explicit command", 500)
				return
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, r)
			if response.Code != http.StatusOK {
				for k, v := range response.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(response.Code)
				_, _ = w.Write(response.Body.Bytes())
				return
			}
			after, e := m6DashboardSnapshot(r.Context(), f)
			if e != nil {
				http.Error(w, "snapshot after command", 500)
				return
			}
			allowed := map[string]bool{"media_request": true, "audit_log": true, "outbox_event": true, "idempotency_record": true}
			for table, hash := range current {
				if !allowed[table] && after[table] != hash {
					http.Error(w, "unrelated table changed", 500)
					return
				}
			}
			var pending int64
			if e = f.pool.QueryRow(r.Context(), `SELECT count(*) FROM media_request WHERE state='pending'`).Scan(&pending); e != nil || pending != 2 {
				http.Error(w, "pending count after rejection", 500)
				return
			}
			baseline = after
			closed++
			for k, v := range response.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
			return
		}
		if r.URL.Path == "/__m9_rates/counters" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int32{"reads": calls.Load(), "unexpected": unexpected.Load()})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__m9_rates/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			current, e := m6DashboardSnapshot(r.Context(), f)
			if e != nil || !reflect.DeepEqual(current, baseline) {
				http.Error(w, "protected snapshot changed before fixture control", http.StatusInternalServerError)
				return
			}
			switch r.URL.Path {
			case "/__m9_rates/failure":
				failed.Store(true)
			case "/__m9_rates/success":
				failed.Store(false)
			default:
				http.NotFound(w, r)
				return
			}
			if e = collect(r.Context()); e != nil {
				http.Error(w, "native fixture collection failed", http.StatusInternalServerError)
				return
			}
			baseline, e = m6DashboardSnapshot(r.Context(), f)
			if e != nil {
				http.Error(w, "snapshot failed", http.StatusInternalServerError)
				return
			}
			changes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			handler.ServeHTTP(w, r)
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
	script := "tests/m9-global-rates-browser.mjs"
	if inbox {
		script = "tests/m9-home-inbox-browser.mjs"
	}
	command := exec.CommandContext(browserCtx, "node", script)
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M9_RATE_BASE="+server.URL, "PULSE_M9_RATE_SESSION="+adminSession, "PULSE_M9_RATE_MEMBER_SESSION="+memberSession, "PULSE_M9_RATE_CDP="+cdp, "PULSE_M9_INBOX_CLOSE_PATH="+closePath)
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if err = command.Run(); err != nil {
		t.Fatalf("native rates Chrome: %v\n%s", err, output.String())
	}
	receipt := "M9_GLOBAL_RATE_CHROME_PASS"
	if inbox {
		receipt = "M9_HOME_INBOX_CHROME_PASS"
	}
	if !strings.Contains(output.String(), receipt) {
		t.Fatal("missing Chrome receipt")
	}
	after, err := m6DashboardSnapshot(ctx, f)
	if err != nil || !reflect.DeepEqual(after, baseline) {
		t.Fatal("browser reads changed protected rows", err)
	}
	if inbox && closed != 1 {
		t.Fatal("expected one explicit persisted rejection", closed)
	}
	if changes != 2 || calls.Load() != 8 || unexpected.Load() != 0 {
		t.Fatal("unexpected native reads, writes or fixture transitions", changes, calls.Load(), unexpected.Load())
	}
	if os.Getenv("PULSE_M9_RATE_REVIEW") == "1" {
		fmt.Printf("M9_RATE_UI_SCREENSHOTS_READY dir=%s\n", shots)
		reviewBudget := 150 * time.Second
		if inbox {
			reviewBudget = 300 * time.Second
		}
		deadline := time.NewTimer(reviewBudget)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, e := os.Stat(filepath.Join(shots, "inspected")); e == nil {
				break
			}
			select {
			case <-deadline.C:
				t.Fatal("screenshot review receipt missing")
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("screenshot review cancelled")
			}
		}
	}
	if inbox {
		t.Log("M9_HOME_INBOX_NATIVE_CHROME_APP_PG_PASS sources=REAL_NATIVE physical_reads=8 browser_writes=1 explicit_rejection=1 pending=3_TO_2 snapshots=FULL_ACCOUNT_TABLES_EXCEPT_AUTH viewports=4")
	} else {
		t.Log("M9_GLOBAL_RATE_NATIVE_CHROME_APP_PG_PASS sources=REAL_NATIVE physical_reads=8 browser_writes=0 snapshots=FULL_ACCOUNT_TABLES_EXCEPT_AUTH viewport_count=4")
	}
}
