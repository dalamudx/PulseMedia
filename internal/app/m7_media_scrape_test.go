package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tmdb"
	"github.com/dalamudx/PulseMedia/internal/adapter/tmdbartwork"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/mediascrape"
	"github.com/dalamudx/PulseMedia/internal/store/mediasidecar"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	scrapeusecase "github.com/dalamudx/PulseMedia/internal/usecase/mediascrape"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests use the real App context, router, Cookie authentication, repository,
// PG18.6 and native filesystem. Inventory is explicitly SQL-prepared, not ingested.
// The native Execute test claims the public command's job through the real queue;
// it does not claim to exercise App.Run's worker lifecycle or external TMDB.
type m7ScrapeFixture struct {
	app    *App
	pool   *pgxpool.Pool
	auth   *auth.Service
	router http.Handler
	cookie *http.Cookie
	admin  uuid.UUID
}

func m7NewScrapeFixture(t *testing.T) *m7ScrapeFixture {
	t.Helper()
	if !strings.HasPrefix(filepath.Clean(os.TempDir()), "/data/temp/") {
		t.Fatal("run with an owned TMPDIR under /data/temp")
	}
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	version, err := postgres.Migrate(t.Context(), dsn, "../../migrations")
	if err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatalf("current migrations required: version=%d err=%v", version, err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var pgVersion int
	if err = pool.QueryRow(t.Context(), `SELECT current_setting('server_version_num')::int`).Scan(&pgVersion); err != nil || pgVersion != 180006 {
		t.Fatalf("acceptance requires PG18.6: version=%d err=%v", pgVersion, err)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	a, err := New(cfg, WithDBPool(pool), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	as := auth.NewService(postgres.NewAuthenticationStore(pool), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	admin, _, err := as.SetupAdmin(t.Context(), "scrape-admin", "OwnedScrapeFixture123!")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewServerHandler(a, as, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetMediaScrapeService(scrapeusecase.NewService(mediascrape.NewRepository(pool), a.IDGen()))
	f := &m7ScrapeFixture{app: a, pool: pool, auth: as, router: httpapi.NewRouter(a, h, as, nil), admin: admin.ID}
	f.cookie = f.login(t, "scrape-admin")
	return f
}

func (f *m7ScrapeFixture) login(t *testing.T, username string) *http.Cookie {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"username": username, "password": "OwnedScrapeFixture123!"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(raw))
	r.Host, r.RemoteAddr = "localhost", "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("real Cookie login: status=%d", w.Code)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "pulsemedia_session" && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatal("login returned no session Cookie")
	return nil
}

func (f *m7ScrapeFixture) request(ctx context.Context, method, path, body, key string, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	r.Host, r.RemoteAddr = "localhost", "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func m7ScrapeCode(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("public scrape status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
	if want == http.StatusOK && w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("scrape response cache policy: %q", w.Header().Get("Cache-Control"))
	}
	if want != http.StatusOK && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("failure is not a problem response: %s", w.Header().Get("Content-Type"))
	}
}

func m7ScrapeBatch(t *testing.T, w *httptest.ResponseRecorder) httpapi.MediaScrapeBatch {
	t.Helper()
	m7ScrapeCode(t, w, http.StatusOK)
	var b httpapi.MediaScrapeBatch
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Id == uuid.Nil || b.CreatedAt.IsZero() || b.Total != b.Queued+b.Running+b.Succeeded+b.Review {
		t.Fatalf("invalid durable batch receipt: %+v", b)
	}
	return b
}

func m7ScrapeStatus(t *testing.T, w *httptest.ResponseRecorder) httpapi.MediaScrapeStatus {
	t.Helper()
	m7ScrapeCode(t, w, http.StatusOK)
	var s httpapi.MediaScrapeStatus
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Include exact rows, not only counts, so GET/rejected requests cannot silently
// rewrite receipts, queue entries, audit or the protected imported graph.
func (f *m7ScrapeFixture) graph(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	out.WriteString(m7ProtectedSidecarGraph(t, f.pool))
	for _, table := range []string{"media_scrape_batch", "media_scrape_item", "job", "audit_log"} {
		var value string
		err := f.pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+pgx.Identifier{table}.Sanitize()+` t) s`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(table)
		out.WriteString(value)
	}
	return out.String()
}

func (f *m7ScrapeFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestM7MediaScrapePublicCookieContractPG(t *testing.T) {
	f := m7NewScrapeFixture(t)
	inventory := f.prepareInventory(t, "movie")
	// A native client can only reach this owned server. GET and POST must never
	// even invoke it; only explicitly executing the queued job may fetch metadata.
	client, calls := m7ScrapeNetwork(t, "movie")
	f.app.metadataHTTPClient = client
	ctx := t.Context()
	before := f.graph(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		m7ScrapeCode(t, f.request(ctx, method, "/api/v1/media/scrape", `{"all":true}`, "anonymous", nil, ""), http.StatusUnauthorized)
	}
	f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.admin)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		m7ScrapeCode(t, f.request(ctx, method, "/api/v1/media/scrape", `{"all":true}`, "member", f.cookie, ""), http.StatusForbidden)
	}
	f.exec(t, `UPDATE app_user SET role='admin' WHERE id=$1`, f.admin)
	if f.graph(t) != before {
		t.Fatal("anonymous/member requests changed business rows")
	}
	actor, err := f.auth.AuthenticateToken(ctx, f.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	tokenSvc := auth.NewTokenManagementService(postgres.NewTokenManagementStore(f.pool, f.app.IDGen()), crypto.ReadTokenIssuer{}, f.app.Clock())
	_, token, err := tokenSvc.Create(ctx, actor, ports.TokenCommand{Name: "scrape read token"}, "scrape-token")
	if err != nil || token == "" {
		t.Fatalf("issue real read token: %v", err)
	}
	// Token creation has its own audit/receipt; snapshot after that authorized work.
	before = f.graph(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		m7ScrapeCode(t, f.request(ctx, method, "/api/v1/media/scrape", `{"all":true}`, "token", nil, token), http.StatusForbidden)
	}
	bodies := []string{"", `{}`, `null`, `[]`, `{"all":false}`, `{"all":null}`, `{"all":"true"}`, `{"All":true}`, `{"all":true,"x":1}`, `{"x":1,"all":true}`, `{"all":true,"all":true}`, `{"all":true,"all":false}`, `{"all":true} {}`, `{"all":true}null`, `{"all":true,}`, `{"all":`, strings.Repeat(" ", 1025) + `{"all":true}`}
	for i, body := range bodies {
		t.Run(fmt.Sprintf("strict_%02d", i), func(t *testing.T) {
			m7ScrapeCode(t, f.request(t.Context(), http.MethodPost, "/api/v1/media/scrape", body, "strict", f.cookie, ""), http.StatusBadRequest)
		})
	}
	for _, key := range []string{"", "bad key", strings.Repeat("a", 129)} {
		m7ScrapeCode(t, f.request(ctx, http.MethodPost, "/api/v1/media/scrape", `{"all":true}`, key, f.cookie, ""), http.StatusBadRequest)
	}
	for _, key := range []string{"bad key", strings.Repeat("a", 129), "bad/key"} {
		m7ScrapeCode(t, f.request(ctx, http.MethodGet, "/api/v1/media/scrape?command_key="+url.QueryEscape(key), "", "", f.cookie, ""), http.StatusBadRequest)
	}
	if f.graph(t) != before {
		t.Fatal("denied/strict requests changed business rows")
	}
	status := m7ScrapeStatus(t, f.request(ctx, http.MethodGet, "/api/v1/media/scrape", "", "", f.cookie, ""))
	if status.Batch != nil || status.Preview.Total != 1 || status.Preview.Missing != 1 {
		t.Fatalf("initial preview: %+v", status)
	}
	// Treat the first POST response as lost; recover through the original key.
	first := m7ScrapeBatch(t, f.request(ctx, http.MethodPost, "/api/v1/media/scrape", `{"all":true}`, "lost-response", f.cookie, ""))
	if first.Total != 1 || first.Queued != 1 {
		t.Fatalf("POST must return queued durable work: %+v", first)
	}
	var durable bool
	err = f.pool.QueryRow(ctx, `SELECT b.requested_by=$2 AND b.command_key='lost-response' AND count(j.id)=1 AND bool_and(j.kind='media_sidecar' AND j.state='queued' AND j.max_attempts=1 AND j.attempts=0 AND j.payload=jsonb_build_object('version',1,'source_event_id',i.source_event_id,'file_id',i.file_id,'unit_id',i.unit_id)) FROM media_scrape_batch b JOIN media_scrape_item i ON i.batch_id=b.id JOIN job j ON j.id=i.job_id WHERE b.id=$1 GROUP BY b.id`, first.Id, f.admin).Scan(&durable)
	if err != nil || !durable {
		t.Fatalf("public receipt not durable native work: %v", err)
	}
	before = f.graph(t)
	for _, path := range []string{"/api/v1/media/scrape?command_key=lost-response", "/api/v1/media/scrape"} {
		got := m7ScrapeStatus(t, f.request(ctx, http.MethodGet, path, "", "", f.cookie, ""))
		if got.Batch == nil || *got.Batch != first || got.Preview.Queued != 1 {
			t.Fatalf("lost response GET: %+v", got)
		}
	}
	replay := m7ScrapeBatch(t, f.request(ctx, http.MethodPost, "/api/v1/media/scrape", `{"all":true}`, "lost-response", f.cookie, ""))
	if replay != first || before != f.graph(t) {
		t.Fatal("same-key POST duplicated or changed original work")
	}
	missing := m7ScrapeStatus(t, f.request(ctx, http.MethodGet, "/api/v1/media/scrape?command_key=absent", "", "", f.cookie, ""))
	if missing.Batch != nil {
		t.Fatal("unknown key fell back to latest batch")
	}
	// Another current administrator cannot recover this account's command key.
	other := uuid.New()
	f.exec(t, `INSERT INTO app_user(id,username,password_hash,role) SELECT $1,'scrape-other',password_hash,'admin' FROM app_user WHERE id=$2`, other, f.admin)
	otherCookie := f.login(t, "scrape-other")
	before = f.graph(t)
	isolated := m7ScrapeStatus(t, f.request(ctx, http.MethodGet, "/api/v1/media/scrape?command_key=lost-response", "", "", otherCookie, ""))
	if isolated.Batch != nil || isolated.Preview.Queued != 1 {
		t.Fatalf("account-specific recovery leaked: %+v", isolated)
	}
	if before != f.graph(t) || calls.Load() != 0 {
		t.Fatal("GET mutated business data or made network requests")
	}
	inventory.assertSource(t)
	t.Log("M7_MEDIA_SCRAPE_PUBLIC_COOKIE_PG_PASS inventory=SQL_PREPARED execution=queued_only")
}

func TestM7MediaScrapeAdminRecheckedAfterLockPG(t *testing.T) {
	for _, lock := range []string{"submission", "app_user"} {
		t.Run(lock, func(t *testing.T) {
			f := m7NewScrapeFixture(t)
			f.prepareInventory(t, "movie")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if lock == "submission" {
				_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(734077)`)
			} else {
				_, err = tx.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, f.admin)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.request(ctx, http.MethodPost, "/api/v1/media/scrape", `{"all":true}`, "demoted-after-wait", f.cookie, "")
			}()
			// Every failure path releases the blocker, cancels and joins the request.
			joined := false
			defer func() {
				_ = tx.Rollback(context.WithoutCancel(ctx))
				cancel()
				if !joined {
					<-done
				}
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case w := <-done:
					joined = true
					t.Fatalf("request did not wait for %s lock: status=%d", lock, w.Code)
				case <-ctx.Done():
					t.Fatal("public submission never reached held database lock")
				case <-ticker.C:
				}
			}
			if lock == "submission" {
				if _, err = tx.Exec(ctx, `UPDATE app_user SET role='member' WHERE id=$1`, f.admin); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := <-done
			joined = true
			m7ScrapeCode(t, w, http.StatusForbidden)
			var empty bool
			if err = f.pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM media_scrape_batch) AND NOT EXISTS(SELECT 1 FROM media_scrape_item) AND NOT EXISTS(SELECT 1 FROM job) AND NOT EXISTS(SELECT 1 FROM audit_log WHERE action='media.scrape.queued')`).Scan(&empty); err != nil || !empty {
				t.Fatalf("demoted request published work: %v", err)
			}
		})
	}
}

func TestM7MediaScrapeRestoreIsolationPG(t *testing.T) {
	f := m7NewScrapeFixture(t)
	f.prepareInventory(t, "movie")
	f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=$1 WHERE singleton`, uuid.New())
	before := f.graph(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		m7ScrapeCode(t, f.request(t.Context(), method, "/api/v1/media/scrape", `{"all":true}`, "isolated", f.cookie, ""), http.StatusServiceUnavailable)
	}
	if f.graph(t) != before {
		t.Fatal("restore isolation allowed business writes")
	}
}

type m7ScrapeInventory struct {
	root, dir, relative, source, nfo string
	identity                         ports.FileIdentity
	original                         []byte
}

func (f *m7ScrapeFixture) prepareInventory(t *testing.T, kind string) m7ScrapeInventory {
	t.Helper()
	v := m7ScrapeInventory{root: t.TempDir(), dir: naming.FormatMovieDir("Sidecar & 原生", 2024), nfo: "movie.nfo", original: []byte("immutable SQL-prepared download bytes\n")}
	v.relative = filepath.Join(v.dir, "source.mkv")
	key := "movie"
	var season, episode any
	if kind == "tv" {
		v.nfo = "tvshow.nfo"
		v.relative = filepath.Join(v.dir, "Season 01", "source.mkv")
		key = "S01E03"
		season = int32(1)
		episode = int32(3)
	}
	if err := os.MkdirAll(filepath.Join(v.root, filepath.Dir(v.relative)), 0700); err != nil {
		t.Fatal(err)
	}
	v.source = filepath.Join(v.root, "download.mkv")
	if err := os.WriteFile(v.source, v.original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(v.source, filepath.Join(v.root, v.relative)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.root, ".pulsemedia_root"), []byte("owned root marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cap, err := rootfs.NewFilesystem().OpenRoot(t.Context(), v.root)
	if err != nil {
		t.Fatal(err)
	}
	defer cap.Close()
	rootIdentity, err := cap.Identity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	v.identity, err = cap.Inspect(t.Context(), v.relative)
	if err != nil {
		t.Fatal(err)
	}
	media, unit, root, file, event, run := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'Sidecar & 原生','Sidecar & 原生',2024,$2)`, media, kind)
	f.exec(t, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,$3,'6211')`, uuid.New(), media, "tmdb."+kind)
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key,season_number,episode_number) VALUES($1,$2,$3,$4,$5)`, unit, media, key, season, episode)
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,$2,$3,'M7 scrape SQL-prepared inventory',true)`, root, v.root, kind)
	f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,$3,$4,$5,$6)`, file, root, v.relative, v.identity.Size, v.identity.Mtime, fmt.Sprintf("stat.v1:%d:%d:%d", v.identity.Inode, v.identity.Size, v.identity.Mtime))
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, unit)
	f.exec(t, `INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), unit, root, file)
	f.exec(t, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) VALUES($1,$2,1,1,0,'succeeded',clock_timestamp())`, run, root)
	f.exec(t, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,$3,$4,true,clock_timestamp() FROM library_root WHERE id=$2`, run, root, rootIdentity.Device, rootIdentity.Inode)
	payload, err := json.Marshal(ports.MediaSidecarPayload{Version: 1, SourceEventID: event, FileID: file, UnitID: unit})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES($1,'media',$2,'media.imported',$3,1)`, event, unit.String(), payload)
	f.exec(t, `INSERT INTO media_artwork(media_id,provider,namespace,external_id,metadata_version,poster_path) SELECT id,'tmdb','tmdb.'||media_type,'6211',metadata_version,'/Sidecar.jpg' FROM media WHERE id=$1`, media)
	return v
}

func (v m7ScrapeInventory) assertSource(t *testing.T) {
	t.Helper()
	for _, path := range []string{v.source, filepath.Join(v.root, v.relative)} {
		if !bytes.Equal(m7ReadRegular(t, path, 0600), v.original) {
			t.Fatal("video source bytes changed")
		}
	}
	a, err := os.Stat(v.source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(v.root, v.relative))
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("source hardlink identity changed")
	}
	cap, err := rootfs.NewFilesystem().OpenRoot(t.Context(), v.root)
	if err != nil {
		t.Fatal(err)
	}
	defer cap.Close()
	identity, err := cap.Inspect(t.Context(), v.relative)
	if err != nil || identity != v.identity {
		t.Fatalf("video inode/size/mtime changed: %v", err)
	}
}

func m7ScrapeNetwork(t *testing.T, kind string) (*http.Client, *atomic.Int32) {
	t.Helper()
	var jpegBytes bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 220, G: 80, B: 30, A: 255})
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Error("native sidecar attempted network mutation")
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/t/p/w342/Sidecar.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpegBytes.Bytes())
		case "/3/" + kind + "/6211":
			if r.URL.Query().Get("api_key") != "owned-sidecar-key" || r.URL.Query().Get("language") != "zh-CN" {
				t.Error("unexpected bound metadata lookup")
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":6211,"title":"Sidecar","name":"Sidecar","original_title":"Sidecar","original_name":"Sidecar","overview":"Owned metadata","release_date":"2024-01-01","first_air_date":"2024-01-01","poster_path":"/Sidecar.jpg","seasons":[]}`)
		default:
			t.Error("unexpected native metadata route")
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: m7SidecarRoute{target, transport}, Timeout: 2 * time.Second}, calls
}

func TestM7MediaScrapeNativeFilesPG(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		for _, preserve := range []bool{false, true} {
			t.Run(fmt.Sprintf("SQLPrepared/%s/preserve_%t", kind, preserve), func(t *testing.T) {
				f := m7NewScrapeFixture(t)
				v := f.prepareInventory(t, kind)
				client, calls := m7ScrapeNetwork(t, kind)
				shared := budget.NewMetadataBudget(f.app.Clock())
				metadata := tmdb.NewClient("owned-sidecar-key", tmdb.WithHTTPClient(client), tmdb.WithRequestBudget(shared))
				svc := metadatasidecar.NewService(mediasidecar.NewRepository(f.pool, f.app.IDGen()), rootfs.NewFilesystem(), metadata, tmdbartwork.New(client, shared))
				paths := []string{filepath.Join(v.root, v.dir, v.nfo), filepath.Join(v.root, v.dir, "poster.jpg")}
				oldInfo := make([]os.FileInfo, len(paths))
				userBytes := []byte("user supplied sidecar bytes must survive unchanged\n")
				if preserve {
					for i, path := range paths {
						if err := os.WriteFile(path, userBytes, 0600); err != nil {
							t.Fatal(err)
						}
						var err error
						oldInfo[i], err = os.Stat(path)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				protected := m7ProtectedSidecarGraph(t, f.pool)
				batch := m7ScrapeBatch(t, f.request(t.Context(), http.MethodPost, "/api/v1/media/scrape", `{"all":true}`, "native-files", f.cookie, ""))
				if batch.Total != 1 || batch.Queued != 1 || calls.Load() != 0 {
					t.Fatalf("POST executed I/O instead of queueing: %+v", batch)
				}
				queue := jobs.NewQueue(f.pool, f.app.IDGen(), f.app.Clock())
				job, err := queue.Claim(t.Context(), []string{ports.MediaSidecarJobKind}, "m7-scrape-native", 2*time.Minute)
				if err != nil || job == nil {
					t.Fatalf("claim public job: %v", err)
				}
				var belongs bool
				if err = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM media_scrape_item WHERE batch_id=$1 AND job_id=$2)`, batch.Id, job.ID).Scan(&belongs); err != nil || !belongs {
					t.Fatalf("claimed job is not public batch item: %v", err)
				}
				running := m7ScrapeStatus(t, f.request(t.Context(), http.MethodGet, "/api/v1/media/scrape?command_key=native-files", "", "", f.cookie, ""))
				if running.Batch == nil || running.Batch.Running != 1 || running.Preview.Running != 1 || calls.Load() != 0 {
					t.Fatalf("running preview: %+v", running)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				if err = svc.Execute(ctx, ports.JobLease{ID: job.ID, Owner: "m7-scrape-native", Token: job.LeaseToken}); err != nil {
					t.Fatal(err)
				}
				m7AssertSidecarState(t, f.pool, job.ID, "succeeded", false)
				want := ports.SidecarCreated
				if preserve {
					want = ports.SidecarPreservedRegular
					for i, path := range paths {
						now, err := os.Stat(path)
						if err != nil || !os.SameFile(oldInfo[i], now) || !bytes.Equal(m7ReadRegular(t, path, 0600), userBytes) {
							t.Fatal("user sidecar inode/bytes/permissions changed")
						}
					}
				} else {
					m7AssertNFO(t, paths[0], map[string]string{"movie": "movie", "tv": "tvshow"}[kind], "Sidecar & 原生", 2024, 6211)
					decoded, err := jpeg.Decode(bytes.NewReader(m7ReadRegular(t, paths[1], 0644)))
					if err != nil || decoded.Bounds() != image.Rect(0, 0, 3, 2) {
						t.Fatalf("canonical native JPEG: %v", err)
					}
				}
				var receipt []byte
				if err = f.pool.QueryRow(t.Context(), `SELECT result FROM job WHERE id=$1`, job.ID).Scan(&receipt); err != nil {
					t.Fatal(err)
				}
				m7AssertArtifacts(t, receipt, v.root, v.dir, map[string]ports.SidecarDisposition{v.nfo: want, "poster.jpg": want})
				if calls.Load() < 2 {
					t.Fatal("native metadata/JPEG boundaries not exercised")
				}
				v.assertSource(t)
				if protected != m7ProtectedSidecarGraph(t, f.pool) {
					t.Fatal("scrape changed protected imported graph")
				}
				before, network := f.graph(t), calls.Load()
				finished := m7ScrapeStatus(t, f.request(t.Context(), http.MethodGet, "/api/v1/media/scrape?command_key=native-files", "", "", f.cookie, ""))
				if finished.Batch == nil || finished.Batch.Id != batch.Id || finished.Batch.Succeeded != 1 || finished.Batch.Queued != 0 || finished.Batch.Running != 0 || finished.Preview.Complete != 1 {
					t.Fatalf("terminal public progress: %+v", finished)
				}
				replay := m7ScrapeBatch(t, f.request(t.Context(), http.MethodPost, "/api/v1/media/scrape", `{"all":true}`, "native-files", f.cookie, ""))
				if replay != *finished.Batch || before != f.graph(t) || network != calls.Load() {
					t.Fatal("terminal GET/original-key retry mutated or requeued work")
				}
				entries, err := os.ReadDir(filepath.Join(v.root, v.dir))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".pulsemedia-sidecar-") {
						t.Fatal("native staging file leaked")
					}
				}
				t.Log("M7_MEDIA_SCRAPE_NATIVE_FILES_PASS inventory=SQL_PREPARED execution=native_claimed_lease network=owned_HTTP_fixture")
			})
		}
	}
}
