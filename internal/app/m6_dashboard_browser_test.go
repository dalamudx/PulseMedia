package app

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediaserver"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestM6DashboardStatesActualChromeHTTPPG(t *testing.T)   { runM6DashboardBrowser(t, "states") }
func TestM6DashboardRecoveryActualChromeHTTPPG(t *testing.T) { runM6DashboardBrowser(t, "recovery") }

// These SQL-prepared observations exercise presentation and real read contracts.
// They do not establish production ingestion, capacity sampling or publication evidence.
func runM6DashboardBrowser(t *testing.T, mode string) {
	t.Helper()
	if os.Getenv("PULSE_M6_DASHBOARD_BROWSER") != "1" {
		t.Skip("explicit isolated dashboard browser fixture required")
	}
	assets, shots := os.Getenv("PULSE_M6_DASHBOARD_ASSETS"), os.Getenv("PULSE_M6_DASHBOARD_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.TempDir()} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), "/data/temp/") {
			t.Fatal("task-owned paths required")
		}
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f := organizationSetup(t)
	ctx := t.Context()
	var version int
	if err := f.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version != 180006 {
		t.Fatal("PostgreSQL 18.6 required")
	}
	if err := postgres.CheckSchemaVersion(ctx, f.pool, postgres.CurrentMigrationVersion); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	application, err := New(cfg, WithDBPool(f.pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	// No App.Run: read-only rendering must not race workers against prepared samples.
	authService := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	admin, adminSession, err := authService.SetupAdmin(ctx, "dashboard-admin", "Dashboard-Owned-123!")
	if err != nil {
		t.Fatal(err)
	}
	member, other := uuid.New(), uuid.New()
	hash, err := crypto.HashPassword("Dashboard-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []uuid.UUID{member, other} {
		m6DashboardExec(t, f, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,$2,$3,'member')`, id, fmt.Sprintf("dashboard-member-%d", i), hash)
	}
	_, memberSession, err := authService.Login(ctx, "dashboard-member-0", "Dashboard-Member-123!")
	if err != nil {
		t.Fatal(err)
	}
	seedM6Dashboard(t, f, member, other)
	key := outboxTestKey
	if err := postgres.EncryptLegacyIndexerConfigurations(ctx, f.pool, crypto.NewSecretProtector(key), usecaseSearch.ClassifyLegacyIndexerProtocol); err != nil {
		t.Fatal("source configuration initialization", err)
	}
	manager := download.NewManager(downloadstore.NewRepository(f.pool, f.gen, key))
	manager.SetClientFactory(NativeDownloadClient)
	syncService := mediaserver.NewSyncService(nil, postgres.NewMediaConfirmationRepository(f.pool, f.gen))
	syncService.SetCredentialDecoder(crypto.NewSecretProtector(key))
	syncService.SetServerFactory(NativeMediaServerClient)
	settingsService := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(key))
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trending/all/day" || r.URL.Query().Get("api_key") != "owned-dashboard-metadata" {
			t.Error("unexpected configured dashboard metadata request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(metadata.Close)
	provider := tmdb.NewClient("owned-dashboard-metadata", tmdb.WithBaseURL(metadata.URL), tmdb.WithHTTPClient(metadata.Client()))
	handler := httpapi.NewServerHandler(application, authService, settingsService, nil, nil, provider, nil, nil, nil, nil, manager, syncService, key)
	indexerStore := postgres.NewIndexerManagementStore(f.pool, f.gen, crypto.NewSecretProtector(key))
	handler.SetIndexerManagementServices(usecaseSearch.NewIndexerManagementService(indexerStore, crypto.NewSecretProtector(key)), usecaseSearch.NewIndexerManagementReceiptService(indexerStore))
	router := httpapi.NewRouter(application, handler, authService, nil)
	files := http.FileServer(http.Dir(assets))
	baseline, err := m6DashboardSnapshot(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var fixtureChanges int
	// Local fixture controls only change prepared rows; all API reads still use the
	// production router, Cookie authentication and App-backed store. Snapshot each
	// deliberate transition so it cannot hide mutations made by GET/navigation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__m6_fixture/") {
			if r.Method != http.MethodPost {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			current, e := m6DashboardSnapshot(r.Context(), f)
			if e != nil || !reflect.DeepEqual(current, baseline) {
				http.Error(w, "business snapshot changed before fixture transition", 500)
				return
			}
			switch r.URL.Path {
			case "/__m6_fixture/demote":
				_, e = f.pool.Exec(r.Context(), "UPDATE app_user SET role='member' WHERE id=$1", admin.ID)
			case "/__m6_fixture/empty":
				var tx pgx.Tx
				tx, e = f.pool.Begin(r.Context())
				if e == nil {
					defer func() { _ = tx.Rollback(r.Context()) }()
					_, e = tx.Exec(r.Context(), "UPDATE unit_fact SET revoked_at=statement_timestamp() WHERE revoked_at IS NULL")
					if e == nil {
						_, e = tx.Exec(r.Context(), "UPDATE download_task SET state='completed',organization_state='completed'")
					}
					if e == nil {
						e = tx.Commit(r.Context())
					}
				}
			default:
				http.NotFound(w, r)
				return
			}
			if e != nil {
				http.Error(w, "fixture transition failed", 500)
				return
			}
			baseline, e = m6DashboardSnapshot(r.Context(), f)
			if e != nil {
				http.Error(w, "fixture snapshot failed", 500)
				return
			}
			fixtureChanges++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			router.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			files.ServeHTTP(w, r)
		default:
			http.ServeFile(w, r, filepath.Join(assets, "index.html"))
		}
	}))
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 210*time.Second)
	defer cancel()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	command := exec.CommandContext(browserCtx, "node", "tests/m6-dashboard-browser.mjs", mode)
	command.Dir = "../../web"
	command.Env = append(os.Environ(), "PULSE_M6_DASHBOARD_BASE="+server.URL, "PULSE_M6_DASHBOARD_SESSION="+adminSession, "PULSE_M6_DASHBOARD_MEMBER_SESSION="+memberSession, "PULSE_M6_DASHBOARD_CDP="+cdp)
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	if err = command.Run(); err != nil {
		t.Fatalf("actual dashboard Chrome: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "M6_DASHBOARD_BROWSER_PASS "+mode+" ") {
		t.Fatal("missing actual browser receipt")
	}
	mu.Lock()
	defer mu.Unlock()
	after, e := m6DashboardSnapshot(ctx, f)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(after, baseline) {
		t.Fatal("read/navigation changed business tables")
	}
	if mode == "recovery" && fixtureChanges != 2 {
		t.Fatalf("fixture transitions=%d want 2", fixtureChanges)
	}
	if mode == "states" && fixtureChanges != 0 {
		t.Fatal("unexpected fixture transition")
	}
	t.Logf("M6_DASHBOARD_APP_COOKIE_PG_PASS %s schema=%d actual_Chrome=true business_writes=0 fixture_transitions=%d source=SQL_prepared_observations ingestion_and_sampler_not_claimed=true", mode, postgres.CurrentMigrationVersion, fixtureChanges)
}

func m6DashboardExec(t *testing.T, f *organizationFixture, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func seedM6Dashboard(t *testing.T, f *organizationFixture, member, other uuid.UUID) {
	t.Helper()
	m6DashboardExec(t, f, "UPDATE library_root SET name='Fixture baseline root' WHERE id=$1", f.root)
	// Four independent counts: 9 movies, 1 series, 2 episodes, 11 physical files.
	// The special and regular episode share one file; an extra copy does not add media.
	for i := 0; i < 10; i++ {
		media, unit, file := uuid.New(), uuid.New(), uuid.New()
		kind, key := "movie", "movie"
		if i == 9 {
			kind, key = "tv", "S00E01"
		}
		title := fmt.Sprintf("Imported %02d", i)
		m6DashboardExec(t, f, `INSERT INTO media(id,title,year,media_type) VALUES($1,$2,2026,$3)`, media, title, kind)
		m6DashboardExec(t, f, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,$3)`, unit, media, key)
		m6DashboardExec(t, f, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,9007199254740993,1)`, file, f.root, fmt.Sprintf("import-%02d.mkv", i))
		m6DashboardExec(t, f, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, unit)
		m6DashboardExec(t, f, `INSERT INTO unit_fact(id,source_file_id,root_id,unit_id,stage,created_at) VALUES($1,$2,$3,$4,'imported',statement_timestamp()-$5*interval '1 minute')`, uuid.New(), file, f.root, unit, i+1)
		if i == 9 {
			second, copyFile := uuid.New(), uuid.New()
			m6DashboardExec(t, f, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'S01E01')`, second, media)
			m6DashboardExec(t, f, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, second)
			m6DashboardExec(t, f, `INSERT INTO unit_fact(id,source_file_id,root_id,unit_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), file, f.root, second)
			m6DashboardExec(t, f, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,'copy.mkv',10,1)`, copyFile, f.root)
			m6DashboardExec(t, f, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, copyFile, second)
			m6DashboardExec(t, f, `INSERT INTO unit_fact(id,source_file_id,root_id,unit_id,stage,created_at) VALUES($1,$2,$3,$4,'imported',statement_timestamp()-interval '1 day')`, uuid.New(), copyFile, f.root, second)
		}
		if i != 2 {
			external := fmt.Sprintf("%d", 1000+i)
			poster := fmt.Sprintf("/Poster_%d.png", i)
			if i == 1 {
				poster = "/Broken.png"
			}
			m6DashboardExec(t, f, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,$3,$4)`, uuid.New(), media, "tmdb."+kind, external)
			m6DashboardExec(t, f, `INSERT INTO media_artwork(media_id,provider,namespace,external_id,metadata_version,poster_path) SELECT id,'tmdb',$2,$3,metadata_version,$4 FROM media WHERE id=$1`, media, "tmdb."+kind, external, poster)
		}
	}
	for _, mode := range []string{"fresh", "full", "stale", "unknown", "offline", "partial", "failed"} {
		root, run := uuid.New(), uuid.New()
		m6DashboardExec(t, f, `INSERT INTO library_root(id,path,name,media_type,health,baseline_completed) VALUES($1,$2,$3,'movie','online',true)`, root, filepath.Join(f.target, mode), "Capacity "+mode)
		m6DashboardExec(t, f, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,created_at,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',statement_timestamp()-interval '2 minutes',statement_timestamp()-interval '2 minutes' FROM library_root WHERE id=$2`, run, root)
		m6DashboardExec(t, f, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,99,true,statement_timestamp()-interval '2 minutes' FROM library_root WHERE id=$2`, run, root)
		if mode != "unknown" {
			available := int64(4007199254740993)
			if mode == "full" {
				available = 0
			}
			minutes := 2
			if mode == "stale" {
				minutes = 45
			}
			m6DashboardExec(t, f, `INSERT INTO root_capacity_observation(scan_run_id,total_bytes,available_bytes,sampled_at) VALUES($1,9007199254740993,$2,statement_timestamp()-$3*interval '1 minute')`, run, available, minutes)
		}
		if mode == "offline" {
			m6DashboardExec(t, f, `UPDATE library_root SET health='offline' WHERE id=$1`, root)
		}
		if mode == "partial" || mode == "failed" {
			m6DashboardExec(t, f, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,created_at) SELECT $1,id,scan_token,generation,mutation_seq,$3,statement_timestamp() FROM library_root WHERE id=$2`, uuid.New(), root, mode)
		}
	}
	var candidate uuid.UUID
	if err := f.pool.QueryRow(t.Context(), "SELECT candidate_id FROM download_task WHERE id=$1", f.taskID).Scan(&candidate); err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{"tracking", "unknown", "review_required", "organizing", "tracking"} {
		media, unit, op := uuid.New(), uuid.New(), uuid.New()
		title := []string{"Member native record", "Member unknown", "Member review", "Member organizing", "Other member secret"}[i]
		owner := member
		if i == 4 {
			owner = other
		}
		m6DashboardExec(t, f, `INSERT INTO media(id,title,media_type) VALUES($1,$2,'movie')`, media, title)
		m6DashboardExec(t, f, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, unit, media)
		m6DashboardExec(t, f, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,created_at) VALUES($1,$2,$3,$4,'user',$5,statement_timestamp()-$6*interval '1 second')`, uuid.New(), op, unit, f.root, owner.String(), i)
		taskState, organization := state, "pending"
		if state == "review_required" {
			taskState, organization = "completed", "review_required"
		}
		if state == "organizing" {
			taskState, organization = "completed", "queued"
		}
		m6DashboardExec(t, f, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,organization_state,save_path,content_path,progress,total_bytes,downloaded_bytes,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'/downloads','/downloads/item.mkv',0.25,100,500,'2020-01-02T03:04:05Z')`, uuid.New(), op, f.client, candidate, strings.Repeat(fmt.Sprintf("%x", i+1), 40), taskState, organization)
	}
}

func m6DashboardSnapshot(ctx context.Context, f *organizationFixture) (map[string][32]byte, error) {
	// Exclude only authentication identity/session state. Hash full row contents,
	// including facts, jobs, tasks, reservations, scans, events and successful audit.
	rows, err := f.pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('app_user','app_session') ORDER BY tablename`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make(map[string][32]byte, len(tables))
	for _, table := range tables {
		var value string
		query := `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM ` + pgx.Identifier{"public", table}.Sanitize() + ` r`
		if err = f.pool.QueryRow(ctx, query).Scan(&value); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", table, err)
		}
		out[table] = sha256.Sum256([]byte(value))
	}
	return out, nil
}
