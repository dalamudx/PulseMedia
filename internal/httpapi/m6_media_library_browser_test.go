package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

// Catalog rows are DISPLAY ONLY. No ingestion, imported/confirmed facts, Wanted,
// authority, completion receipt or filesystem content is manufactured here.
func TestM6MediaLibraryActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M6_MEDIA_LIBRARY_BROWSER") != "1" {
		t.Skip("use scripts/verify_m6_media_library_ui.sh")
	}
	repo, assets, shots := os.Getenv("PULSE_M6_MEDIA_LIBRARY_REPO"), os.Getenv("PULSE_M6_MEDIA_LIBRARY_ASSETS"), os.Getenv("PULSE_M6_MEDIA_LIBRARY_SCREENSHOTS")
	for _, p := range []string{assets, shots, os.Getenv("TMPDIR")} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/data/temp/") {
			t.Fatal("owned /data/temp paths required")
		}
	}
	if !filepath.IsAbs(repo) || postgres.CurrentMigrationVersion != 93 {
		t.Fatal("absolute repo and schema93 required")
	}
	t.Setenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE", "1")
	f, native := newM5CreationAcceptance(t)
	until := time.Now().Add(25 * time.Second)
	for m5InteractiveInt(t, f, `SELECT count(*) FROM job WHERE state IN ('queued','running')`) != 0 {
		if time.Now().After(until) {
			t.Fatal("initial root jobs failed to settle")
		}
		time.Sleep(50 * time.Millisecond)
	}
	sql := func(q string, args ...any) {
		t.Helper()
		if _, e := f.app.DBPool().Exec(t.Context(), q, args...); e != nil {
			t.Fatal(e)
		}
	}
	var admin uuid.UUID
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM app_user WHERE username='creation-admin'`).Scan(&admin); e != nil {
		t.Fatal(e)
	}
	member := uuid.New()
	sql(`INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'library-member',password_hash,'member',true FROM app_user WHERE id=$2`, member, admin)
	memberSession := m5CreationLogin(t, f, "library-member")
	issued := f.request(t, "POST", "/api/v1/auth/tokens", uuid.NewString(), map[string]string{"name": "library-no-read"}, f.session)
	m5IndexerStatus(t, issued, 200)
	var token struct {
		Token string `json:"secret"`
	}
	if e := json.Unmarshal(issued.body, &token); e != nil || token.Token == "" {
		t.Fatal("real scoped token issuance failed", e)
	}
	sql(`UPDATE api_token SET scopes='["write"]'::jsonb`)
	for _, kind := range []string{"movie", "tv"} {
		var root uuid.UUID
		if e := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type=$1 LIMIT 1`, kind).Scan(&root); e != nil {
			t.Fatal(e)
		}
		sql(`UPDATE library_root SET name=$2 WHERE id=$1`, root, "家庭影音资料馆-"+strings.Repeat("LongReadableRootName", 7))
		media := uuid.New()
		title := "Library Display Movie — 长片名与真实相对路径边界"
		units := []string{"movie"}
		if kind == "tv" {
			title = "Library Display Series — 稀疏季集与文件关联"
			units = []string{"S01E02", "S01E10", "S03E07"}
		}
		sql(`INSERT INTO media(id,title,media_type,year) VALUES($1,$2,$3,2024)`, media, title, kind)
		for _, key := range units {
			unit, file := uuid.New(), uuid.New()
			rel := "家庭影音/" + strings.Repeat("Long.Relative.Filename.", 10) + "/" + key + ".2160p.WEB-DL.mkv"
			sql(`INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,$3)`, unit, media, key)
			sql(`INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,9223372036854775807,1)`, file, root, rel)
			sql(`INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, file, unit)
		}
	}
	rows, e := f.app.DBPool().Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('goose_db_version','app_user','app_session','api_token') ORDER BY tablename`)
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
	baseline, nativeBefore := snapshot(), native.count()
	var apiReads, apiWrites atomic.Int64
	var mu sync.Mutex
	down, denied := false, false
	defer func() {
		if down {
			if _, e := f.app.DBPool().Exec(context.Background(), `ALTER TABLE owned_m6_library_mapping_unavailable RENAME TO library_file_unit`); e != nil {
				t.Error(e)
			}
		}
	}()
	target, _ := url.Parse(f.base)
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) { p.SetURL(target); p.Out.Host = target.Host }}
	files := http.FileServer(http.Dir(assets))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method == "GET" {
				apiReads.Add(1)
			} else {
				apiWrites.Add(1)
			}
			mu.Lock()
			deny := denied
			mu.Unlock()
			// This is a real production scope denial, not a forged 403 response.
			if deny && r.URL.Path == "/api/v1/library/catalog" {
				r.Header.Del("Cookie")
				r.Header.Set("Authorization", "Bearer "+token.Token)
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__library/") {
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			var err error
			switch strings.TrimPrefix(r.URL.Path, "/__library/") {
			case "check":
				if !reflect.DeepEqual(baseline, snapshot()) {
					err = fmt.Errorf("read-only business snapshot changed")
				}
			case "storage-down":
				if down {
					err = fmt.Errorf("already down")
				} else {
					_, err = f.app.DBPool().Exec(r.Context(), `ALTER TABLE library_file_unit RENAME TO owned_m6_library_mapping_unavailable`)
					down = err == nil
				}
			case "storage-up":
				if !down {
					err = fmt.Errorf("not down")
				} else {
					_, err = f.app.DBPool().Exec(r.Context(), `ALTER TABLE owned_m6_library_mapping_unavailable RENAME TO library_file_unit`)
					down = err != nil
				}
			case "deny":
				denied = true
			case "allow":
				denied = false
			case "demote":
				_, err = f.app.DBPool().Exec(r.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, admin)
			case "restore":
				_, err = f.app.DBPool().Exec(r.Context(), `UPDATE app_user SET role='admin' WHERE id=$1`, admin)
			case "revoke":
				_, err = f.app.DBPool().Exec(r.Context(), `UPDATE app_session SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, admin)
			default:
				w.WriteHeader(404)
				return
			}
			if err != nil {
				t.Error(err)
				http.Error(w, "fixture checkpoint failed", 500)
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
			c := &http.Client{Timeout: time.Second}
			defer c.CloseIdleConnections()
			if r, e := c.Get(cdp + "/json/version"); e == nil {
				_ = r.Body.Close()
				t.Error("Chrome remains reachable")
			} else {
				t.Log("M6_MEDIA_LIBRARY_CHROME_CLEANUP_VERIFIED")
			}
		})
	}
	defer stop()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/tests/media-library-browser.mjs"))
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M6_MEDIA_LIBRARY_BASE="+bridge.URL, "PULSE_M6_MEDIA_LIBRARY_SESSION="+f.session, "PULSE_M6_MEDIA_LIBRARY_MEMBER_SESSION="+memberSession, "PULSE_M6_MEDIA_LIBRARY_CDP="+cdp)
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, os.Stdout)
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	stop()
	if err != nil {
		t.Fatalf("actual Chrome: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "M6_MEDIA_LIBRARY_BROWSER_PASS ") {
		t.Fatal("missing browser receipt")
	}
	if down || !reflect.DeepEqual(baseline, snapshot()) || native.count() != nativeBefore || apiWrites.Load() != 0 || apiReads.Load() < 15 {
		t.Fatal("terminal read-only/native/HTTP boundary failed")
	}
	t.Logf("M6_MEDIA_LIBRARY_APP_COOKIE_PG_PASS schema=93 api_GET=%d business_writes=0 native_calls=0 SQL_display_only=true genuine_ingestion=false linked_not_imported=true", apiReads.Load())
}
