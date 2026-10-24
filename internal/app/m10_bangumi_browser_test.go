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

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/bangumi"
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
	"github.com/dalamudx/PulseMedia/internal/usecase/calendar"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadata"
	"github.com/dalamudx/PulseMedia/internal/usecase/policy"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func TestM10BangumiChromePG(t *testing.T) {
	if os.Getenv("PULSE_M10_BANGUMI_BROWSER") != "1" {
		t.Skip("explicit browser fixture")
	}
	assets, shots := os.Getenv("PULSE_M10_BANGUMI_ASSETS"), os.Getenv("PULSE_M10_BANGUMI_SHOTS")
	for _, p := range []string{assets, shots, os.TempDir()} {
		real, e := filepath.EvalSymlinks(p)
		if e != nil || real != filepath.Clean(p) || !strings.HasPrefix(p, "/data/temp/") {
			t.Fatal("owned paths required")
		}
	}
	f := organizationSetup(t)
	ctx := t.Context()
	root := uuid.New()
	rootPath := t.TempDir()
	if e := os.WriteFile(filepath.Join(rootPath, ".pulsemedia_root"), []byte("owned TV root"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: rootPath, Name: "Anime", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e := library.NewScanner(f.gen, rootfs.NewFilesystem(), postgres.NewScanObservationStore(f.pool)).ScanRoot(ctx, root); e != nil {
		t.Fatal(e)
	}
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch r.URL.Path {
		case "/calendar":
			days := make([]any, 7)
			for i := range days {
				items := []any{}
				if i == 0 {
					items = append(items, map[string]any{"id": 12345, "type": 2, "name_cn": "周历测试番剧", "name": "カレンダーの物語", "air_date": "2023-09-29", "rating": map[string]any{"score": 8.9}})
				}
				if i == 6 {
					items = append(items, map[string]any{"id": 98765, "type": 2, "name_cn": "首播未知的新番", "name": "未定のアニメ"})
				}
				days[i] = map[string]any{"weekday": map[string]int{"id": i + 1}, "items": items}
			}
			raw, _ := json.Marshal(days)
			body = string(raw)
		case "/3/search/tv", "/3/search/multi", "/3/trending/all/day", "/3/trending/all/week", "/3/trending/movie/week":
			body = `{"page":1,"total_pages":1,"total_results":1,"results":[{"id":42,"name":"周历测试番剧","original_name":"カレンダーの物語","media_type":"tv","overview":"正式周历电视剧资料","first_air_date":"2023-09-29","vote_average":8.9}]}`
		case "/3/tv/42":
			body = `{"id":42,"name":"周历测试番剧","original_name":"カレンダーの物語","overview":"正式周历电视剧资料","first_air_date":"2023-09-29","vote_average":8.9,"genres":[{"id":16,"name":"动画"}],"number_of_seasons":1,"number_of_episodes":2,"seasons":[{"season_number":1,"name":"第一季","episode_count":2}]}`
		case "/3/tv/42/season/1":
			body = `{"id":421,"season_number":1,"episodes":[{"id":422,"season_number":1,"episode_number":2,"name":"第二集","air_date":null},{"id":427,"season_number":1,"episode_number":7,"name":"第七集","air_date":null}]}`
		case "/3/tv/42/credits":
			body = `{"id":42,"cast":[]}`
		default:
			t.Error("unexpected native metadata endpoint", r.URL.Path)
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer native.Close()
	provider := tmdb.NewClient("owned-calendar-key", tmdb.WithBaseURL(native.URL+"/3"), tmdb.WithHTTPClient(native.Client()))
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	app, e := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = app.Close() })
	authentication := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	_, session, e := authentication.SetupAdmin(ctx, "bangumi-admin", "Owned-Calendar-Admin-123!")
	if e != nil {
		t.Fatal(e)
	}
	hash, e := crypto.HashPassword("Owned-Calendar-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "bangumi-member", PasswordHash: hash, Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	_, member, e := authentication.Login(ctx, "bangumi-member", "Owned-Calendar-Member-123!")
	if e != nil {
		t.Fatal(e)
	}
	handler := httpapi.NewServerHandler(app, authentication, nil, nil, nil, provider, nil, policy.NewService(postgres.NewPolicyStore(f.pool, f.gen)), nil, nil, f.mgr, nil, outboxTestKey)
	protector := crypto.NewSecretProtector(outboxTestKey)
	var sourceCalls atomic.Int32
	ownedSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sourceCalls.Add(1)
		http.Error(w, "unexpected owned source request", http.StatusServiceUnavailable)
	}))
	defer ownedSource.Close()
	endpointSecret, e := protector.EncryptSecret([]byte("Owned-Calendar-Endpoint-123!"))
	if e != nil {
		t.Fatal(e)
	}
	m6DashboardExec(t, f, "UPDATE download_client SET url=$1,"+"pass"+"word_encrypted=$2", ownedSource.URL, endpointSecret)
	m6DashboardExec(t, f, "UPDATE indexer SET url=$1", ownedSource.URL)
	if e = postgres.EncryptLegacyIndexerConfigurations(ctx, f.pool, protector, usecaseSearch.ClassifyLegacyIndexerProtocol); e != nil {
		t.Fatal("seal owned source configuration", e)
	}
	indexerStore := postgres.NewIndexerManagementStore(f.pool, f.gen, protector)
	handler.SetIndexerManagementServices(usecaseSearch.NewIndexerManagementService(indexerStore, protector), usecaseSearch.NewIndexerManagementReceiptService(indexerStore))
	handler.SetMetadataDetailService(metadata.NewDetailService(provider))
	handler.SetBangumiCalendarService(calendar.NewService(bangumi.NewClient(native.URL, native.Client()), clock.New()))
	handler.SetRootSelectionService(subscription.NewRootSelectionService(postgres.NewLibraryRootSelectionStore(f.pool)))
	handler.SetSubscriptionCreationService(subscription.NewSubscriptionCreationService(postgres.NewSubscriptionCreationStore(f.pool, f.gen), provider, provider, crypto.NewSecretProtector(outboxTestKey)))
	router := httpapi.NewRouter(app, handler, authentication, nil)
	for _, path := range []string{"/api/v1/indexers", "/api/v1/download-clients"} {
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		reply := httptest.NewRecorder()
		router.ServeHTTP(reply, request)
		if reply.Code != http.StatusOK {
			t.Fatalf("binding preflight %s status=%d body=%s", path, reply.Code, reply.Body.String())
		}
	}
	var mu sync.Mutex
	writes := 0
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		before, e := m6DashboardSnapshot(context.WithoutCancel(r.Context()), f)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		record := httptest.NewRecorder()
		router.ServeHTTP(record, r)
		after, e := m6DashboardSnapshot(context.WithoutCancel(r.Context()), f)
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		if r.Method != http.MethodGet {
			writes++
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/subscriptions" {
				t.Error("unexpected browser mutation", r.Method, r.URL.Path)
			}
		} else if !reflect.DeepEqual(before, after) {
			t.Error("GET changed business graph", r.URL.Path)
		}
		for key, values := range record.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(record.Code)
		_, _ = w.Write(record.Body.Bytes())
	}))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	setup, _ := json.Marshal(map[string]any{"base": server.URL, "admin": session, "member": member, "root": root})
	command := exec.CommandContext(browserCtx, "node", "tests/m10-bangumi-browser.mjs")
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M10_BANGUMI_SETUP="+string(setup), "PULSE_M10_BANGUMI_CDP="+cdp)
	command.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if e = command.Run(); e != nil {
		t.Fatalf("calendar Chrome: %v\n%s", e, output.String())
	}
	if strings.Count(output.String(), "M10_BANGUMI_CHROME_PASS") != 1 {
		t.Fatal("browser receipt missing")
	}
	var subscriptions, wanted int
	if e = f.pool.QueryRow(ctx, `SELECT count(*) FROM subscription s JOIN media m ON m.id=s.media_id WHERE m.media_type='tv'`).Scan(&subscriptions); e != nil || subscriptions != 1 {
		t.Fatal("subscription count", subscriptions, e)
	}
	if e = f.pool.QueryRow(ctx, `SELECT count(*) FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE u.unit_key IN ('S01E02','S01E07')`).Scan(&wanted); e != nil || wanted != 2 {
		t.Fatal("sparse Wanted missing", wanted, e)
	}
	if sourceCalls.Load() != 0 {
		t.Fatal("calendar or subscription called configured downloader/indexer", sourceCalls.Load())
	}
	if writes != 1 {
		t.Fatal("unexpected command count", writes)
	}
	if os.Getenv("PULSE_M10_BANGUMI_REVIEW") == "1" {
		fmt.Printf("M10_BANGUMI_SCREENSHOTS_READY dir=%s\n", shots)
		timer := time.NewTimer(300 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, e := os.Stat(filepath.Join(shots, "inspected")); e == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("review cancelled")
			case <-timer.C:
				t.Fatal("review absent")
			case <-tick.C:
			}
		}
	}
	t.Log("M10_BANGUMI_BROWSER_PG_PASS Cookie=REAL calendar=READ_ONLY TMDB=EXPLICIT_SELECTION subscription=ONE sparse_wanted=2 unknown_dates=PRESERVED workers=NOT_STARTED")
}
