package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// SQL-prepared inventory tests UI commands and recorded states, never ingestion,
// filesystem publication or genuine NFO/JPEG. No invented file is a success proof.
func TestM7MediaScrapeActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M7_MEDIA_SCRAPE_BROWSER") != "1" {
		t.Skip("use scripts/verify_m7_media_scrape_ui.sh")
	}
	repo, assets, shots := os.Getenv("PULSE_M7_MEDIA_SCRAPE_REPO"), os.Getenv("PULSE_M7_MEDIA_SCRAPE_ASSETS"), os.Getenv("PULSE_M7_MEDIA_SCRAPE_SCREENSHOTS")
	for _, p := range []string{assets, shots, os.Getenv("TMPDIR")} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/data/temp/") {
			t.Fatal("owned /data/temp paths required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repository required")
	}
	t.Setenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE", "1")
	f, native := newM5CreationAcceptance(t)
	until := time.Now().Add(25 * time.Second)
	for m5InteractiveInt(t, f, `SELECT count(*) FROM job WHERE state IN ('queued','running')`) != 0 {
		if time.Now().After(until) {
			t.Fatal("initial jobs did not settle")
		}
		time.Sleep(50 * time.Millisecond)
	}
	sql := func(q string, args ...any) {
		t.Helper()
		if _, e := f.app.DBPool().Exec(t.Context(), q, args...); e != nil {
			t.Fatal(e)
		}
	}
	var admin, root uuid.UUID
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM app_user WHERE username='creation-admin'`).Scan(&admin); e != nil {
		t.Fatal(e)
	}
	sql(`INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'scrape-member',password_hash,'member',true FROM app_user WHERE id=$2`, uuid.New(), admin)
	memberSession := m5CreationLogin(t, f, "scrape-member")
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='movie' LIMIT 1`).Scan(&root); e != nil {
		t.Fatal(e)
	}
	// Explicit SQL observations fill missing/queued/review badges. Omit media_identity:
	// workers must fail validation before metadata I/O, never report native success.
	for i, status := range []string{"missing", "queued", "dead"} {
		media, unit, file, event := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		sql(`INSERT INTO media(id,title,media_type,year) VALUES($1,$2,'movie',2024)`, media, fmt.Sprintf("SQL prepared scrape UI %d", i))
		sql(`INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, unit, media)
		sql(`INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,1024,1)`, file, root, fmt.Sprintf("SQL-prepared-no-files/Film-%d/movie.mkv", i))
		sql(`INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, unit)
		sql(`INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), unit, root, file)
		sql(`INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq) VALUES($1,'media',$2,'media.imported',jsonb_build_object('version',1,'source_event_id',$1::uuid,'file_id',$3::uuid,'unit_id',$4::uuid),1)`, event, unit.String(), file, unit)
		if status != "missing" {
			sql(`INSERT INTO job(id,kind,state,payload,max_attempts,not_before) VALUES($1,'media_sidecar',$2,jsonb_build_object('version',1,'source_event_id',$3::uuid,'file_id',$4::uuid,'unit_id',$5::uuid),1,clock_timestamp()+interval '1 day')`, uuid.New(), status, event, file, unit)
		}
	}
	rows, e := f.app.DBPool().Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('goose_db_version','app_user','app_session','api_token','job','audit_log','media_scrape_batch','media_scrape_item') ORDER BY tablename`)
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, name)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			var raw string
			q := fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]')::text FROM %s x`, table)
			if e := f.app.DBPool().QueryRow(t.Context(), q).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			out[table] = raw
		}
		return out
	}
	baseline, calls := snapshot(), native.count()
	var oldJobs, oldAudit string
	for q, dest := range map[string]*string{`SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY id),'[]')::text FROM job j`: &oldJobs, `SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY id),'[]')::text FROM audit_log j`: &oldAudit} {
		if e := f.app.DBPool().QueryRow(t.Context(), q).Scan(dest); e != nil {
			t.Fatal(e)
		}
	}
	var mu sync.Mutex
	down := false
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if down {
			_, e := f.app.DBPool().Exec(context.Background(), `ALTER VIEW owned_m7_scrape_unavailable RENAME TO media_scrape_candidate`)
			if e != nil {
				t.Error(e)
			}
		}
	}()
	target, _ := url.Parse(f.base)
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(target)
		p.Out.Host = target.Host
		if p.In.Header.Get("Origin") == "http://"+p.In.Host {
			p.Out.Header.Set("Origin", f.base)
		}
	}}
	var reads, writes, forbidden atomic.Int64
	files := http.FileServer(http.Dir(assets))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method == "GET" {
				reads.Add(1)
			} else {
				writes.Add(1)
				if r.Method != "POST" || r.URL.Path != "/api/v1/media/scrape" {
					forbidden.Add(1)
				}
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__scrape/") {
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			var err error
			switch strings.TrimPrefix(r.URL.Path, "/__scrape/") {
			case "storage-down":
				if down {
					err = fmt.Errorf("already down")
				} else {
					_, err = f.app.DBPool().Exec(r.Context(), `ALTER VIEW media_scrape_candidate RENAME TO owned_m7_scrape_unavailable`)
					down = err == nil
				}
			case "storage-up":
				if !down {
					err = fmt.Errorf("already up")
				} else {
					_, err = f.app.DBPool().Exec(r.Context(), `ALTER VIEW owned_m7_scrape_unavailable RENAME TO media_scrape_candidate`)
					down = err != nil
				}
			default:
				w.WriteHeader(404)
				return
			}
			if err != nil {
				t.Error(err)
				http.Error(w, "fixture failed", 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(bridge.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 160*time.Second)
	defer cancel()
	cdp, stopChrome := startFileMappingBrowser(t, ctx)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			stopChrome()
			client := &http.Client{Timeout: time.Second}
			defer client.CloseIdleConnections()
			if r, e := client.Get(cdp + "/json/version"); e == nil {
				_ = r.Body.Close()
				t.Error("Chrome remains reachable")
			} else {
				t.Log("M7_MEDIA_SCRAPE_CHROME_CLEANUP_VERIFIED")
			}
		})
	}
	defer stop()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/tests/m7-media-scrape-browser.mjs"))
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M7_MEDIA_SCRAPE_BASE="+bridge.URL, "PULSE_M7_MEDIA_SCRAPE_SESSION="+f.session, "PULSE_M7_MEDIA_SCRAPE_MEMBER_SESSION="+memberSession, "PULSE_M7_MEDIA_SCRAPE_CDP="+cdp)
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, os.Stdout)
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	stop()
	if err != nil {
		t.Fatalf("Chrome acceptance: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "M7_MEDIA_SCRAPE_BROWSER_PASS ") {
		t.Fatal("missing browser receipt")
	}
	mu.Lock()
	storageDown := down
	mu.Unlock()
	if storageDown || !reflect.DeepEqual(baseline, snapshot()) || native.count() != calls || writes.Load() != 1 || forbidden.Load() != 0 || reads.Load() < 5 {
		t.Fatal("protected inventory/native/HTTP boundary changed")
	}
	var stable bool
	err = f.app.DBPool().QueryRow(t.Context(), `SELECT
 (SELECT count(*)=1 FROM media_scrape_batch) AND (SELECT count(*)=1 FROM media_scrape_item)
 AND (SELECT count(*)=1 FROM audit_log WHERE action='media.scrape.queued')
 AND NOT EXISTS(SELECT 1 FROM job WHERE kind='media_sidecar' AND state='succeeded')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements($1::jsonb) old WHERE NOT EXISTS(SELECT 1 FROM job j WHERE to_jsonb(j)=old))
 AND NOT EXISTS(SELECT 1 FROM job j WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements($1::jsonb) old WHERE old->>'id'=j.id::text) AND NOT EXISTS(SELECT 1 FROM media_scrape_item i WHERE i.job_id=j.id))
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements($2::jsonb) old WHERE NOT EXISTS(SELECT 1 FROM audit_log a WHERE to_jsonb(a)=old))
 AND NOT EXISTS(SELECT 1 FROM audit_log a WHERE a.action!='media.scrape.queued' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements($2::jsonb) old WHERE old=to_jsonb(a)))`, oldJobs, oldAudit).Scan(&stable)
	if err != nil || !stable {
		t.Fatal("batch/job/audit additions boundary", err)
	}
	t.Logf("M7_MEDIA_SCRAPE_APP_COOKIE_PG_PASS schema=94 api_GET=%d api_POST=1 native_calls=0 SQL_prepared=true genuine_ingestion=false filesystem_success=false", reads.Load())
}
