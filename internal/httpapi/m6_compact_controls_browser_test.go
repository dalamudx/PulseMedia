package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
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

	"github.com/jackc/pgx/v5"
)

// No SQL display rows, runtime histories, ingestion or command authority are seeded.
func TestM6CompactControlsActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M6_COMPACT_CONTROLS_BROWSER") != "1" {
		t.Skip("use scripts/verify_m6_compact_controls_ui.sh")
	}
	repo, assets, shots := os.Getenv("PULSE_M6_COMPACT_CONTROLS_REPO"), os.Getenv("PULSE_M6_COMPACT_CONTROLS_ASSETS"), os.Getenv("PULSE_M6_COMPACT_CONTROLS_SCREENSHOTS")
	for _, p := range []string{assets, shots, os.Getenv("TMPDIR")} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/data/temp/") {
			t.Fatal("owned /data/temp paths required")
		}
	}
	if !filepath.IsAbs(repo) {
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
	// Snapshot every unrelated table, including jobs/downloads and root/scan facts.
	rows, err := f.app.DBPool().Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('goose_db_version','app_session','api_token','policy','policy_revision','audit_log','outbox_event','idempotency_record') ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	snapshot := func() map[string][32]byte {
		out := map[string][32]byte{}
		for _, table := range tables {
			var raw string
			q := fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]')::text FROM %s x`, pgx.Identifier{table}.Sanitize())
			if e := f.app.DBPool().QueryRow(t.Context(), q).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			out[table] = sha256.Sum256([]byte(raw))
		}
		return out
	}
	filesystem := func() map[string][32]byte {
		out := map[string][32]byte{}
		roots, e := f.app.DBPool().Query(t.Context(), `SELECT path FROM library_root ORDER BY id`)
		if e != nil {
			t.Fatal(e)
		}
		defer roots.Close()
		for roots.Next() {
			var root string
			if e = roots.Scan(&root); e != nil {
				t.Fatal(e)
			}
			e = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				info, e := d.Info()
				if e != nil {
					return e
				}
				metadata := fmt.Sprintf("%s:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
				if d.Type().IsRegular() {
					b, e := os.ReadFile(path)
					if e != nil {
						return e
					}
					metadata += fmt.Sprintf(":%x", sha256.Sum256(b))
				}
				out[path] = sha256.Sum256([]byte(metadata))
				return nil
			})
			if e != nil {
				t.Fatal("root filesystem snapshot failed")
			}
		}
		if e = roots.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	counts := func() [5]int {
		var v [5]int
		if e := f.app.DBPool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM api_token),(SELECT count(*) FROM policy_revision),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record)`).Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); e != nil {
			t.Fatal(e)
		}
		return v
	}
	baseline, filesBefore, before, nativeBefore := snapshot(), filesystem(), counts(), native.count()
	var writes, forbidden atomic.Int64
	target, _ := url.Parse(f.base)
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(target)
		p.Out.Host = target.Host
		if p.In.Header.Get("Origin") == "http://"+p.In.Host {
			p.Out.Header.Set("Origin", f.base)
		}
	}}
	static := http.FileServer(http.Dir(assets))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != "GET" {
				allowed := r.Method == "POST" && (r.URL.Path == "/api/v1/auth/tokens" || (strings.HasPrefix(r.URL.Path, "/api/v1/auth/tokens/") && strings.HasSuffix(r.URL.Path, "/revoke")) || r.URL.Path == "/api/v1/policies/default/revisions")
				if !allowed {
					forbidden.Add(1)
					w.WriteHeader(405)
					return
				}
				writes.Add(1)
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			static.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(bridge.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
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
				t.Log("M6_COMPACT_CONTROLS_CHROME_CLEANUP_VERIFIED")
			}
		})
	}
	defer stop()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/tests/m6-compact-controls-browser.mjs"))
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M6_COMPACT_CONTROLS_BASE="+bridge.URL, "PULSE_M6_COMPACT_CONTROLS_SESSION="+f.session, "PULSE_M6_COMPACT_CONTROLS_CDP="+cdp)
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, os.Stdout)
	cmd.Stderr = cmd.Stdout
	err = cmd.Run()
	stop()
	if err != nil {
		t.Fatalf("actual Chrome failed: %v (browser output intentionally withheld)", err)
	}
	if !strings.Contains(output.String(), "M6_COMPACT_CONTROLS_BROWSER_PASS ") {
		t.Fatal("missing browser receipt")
	}
	if !reflect.DeepEqual(baseline, snapshot()) || !reflect.DeepEqual(filesBefore, filesystem()) || native.count() != nativeBefore || writes.Load() != 7 || forbidden.Load() != 0 {
		t.Fatal("unrelated business/filesystem/native/HTTP boundary changed")
	}
	after := counts()
	want := [5]int{2, 1, 4, 4, 4}
	for i := range after {
		if after[i]-before[i] != want[i] {
			t.Fatalf("durable command ledger delta mismatch at %d", i)
		}
	}
	var revision, revisions, revoked, distinctKeys int
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT (SELECT current_revision FROM policy WHERE id='default'),(SELECT count(*) FROM policy_revision WHERE policy_id='default'),(SELECT count(*) FROM api_token WHERE revoked_at IS NOT NULL),(SELECT count(DISTINCT idempotency_key) FROM idempotency_record)`).Scan(&revision, &revisions, &revoked, &distinctKeys); e != nil {
		t.Fatal(e)
	}
	if revision != 2 || revisions != 2 || revoked != 1 || distinctKeys != after[4] {
		t.Fatal("duplicate revision/token/receipt")
	}
	t.Log("M6_COMPACT_CONTROLS_APP_COOKIE_PG_PASS schema=93 PG=18.6 commands=4 retries=3 unrelated_business_writes=0 filesystem_changes=0 native_calls=0 SQL_display_only=false")
}
