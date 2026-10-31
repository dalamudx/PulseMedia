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
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// SQL rows below are read projections, not evidence of acquisition or file completion.
func TestM6OperationActivityActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M6_OPERATION_BROWSER") != "1" {
		t.Skip("use scripts/verify_m6_operation_activity_ui.sh")
	}
	repo, assets, shots := os.Getenv("PULSE_M6_OPERATION_REPO"), os.Getenv("PULSE_M6_OPERATION_ASSETS"), os.Getenv("PULSE_M6_OPERATION_SCREENSHOTS")
	for _, p := range []string{assets, shots, os.Getenv("TMPDIR")} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/data/temp/") {
			t.Fatal("owned paths required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repo and schema93 required")
	}
	t.Setenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE", "1")
	f, _ := newM5CreationAcceptance(t)
	// No subscription or acquisition is started. App owns only the initial root jobs.
	until := time.Now().Add(20 * time.Second)
	for m5InteractiveInt(t, f, `SELECT count(*) FROM job WHERE state IN ('queued','running')`) != 0 {
		if time.Now().After(until) {
			t.Fatal("initial root jobs did not settle")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var native atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		native.Add(1)
		// App.Run also owns periodic observation of the SQL-prepared tracking rows.
		// A failed background login changes only the independent display sample.
		// GET-only native isolation is measured by the separate public API suite.
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/auth/login" {
			t.Errorf("unexpected non-observation native endpoint %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	clientResult := f.request(t, "POST", "/api/v1/download-clients", "", map[string]any{"name": "Operation owned native client", "client_type": "qbittorrent", "url": upstream.URL, "username": "owned-user", "password": uuid.NewString()}, f.session)
	m5IndexerStatus(t, clientResult, 201)
	client := m5CreationID(t, clientResult, "id")
	var admin, root uuid.UUID
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM app_user WHERE username='creation-admin'`).Scan(&admin); e != nil {
		t.Fatal(e)
	}
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='movie' LIMIT 1`).Scan(&root); e != nil {
		t.Fatal(e)
	}
	member := uuid.New()
	sql := func(q string, args ...any) {
		t.Helper()
		if _, e := f.app.DBPool().Exec(t.Context(), q, args...); e != nil {
			t.Fatal(e)
		}
	}
	sql(`INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'operation-member',password_hash,'member',true FROM app_user WHERE id=$2`, member, admin)
	memberSession := m5CreationLogin(t, f, "operation-member")
	media, unit, indexer, candidate := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sql(`INSERT INTO media(id,title,media_type) VALUES($1,'Operation display fixture with a deliberately long readable title','movie')`, media)
	sql(`INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, unit, media)
	// Candidate is a display fixture only; no query proof, authority or submission job exists.
	sql(`INSERT INTO indexer(id,name,url,api_key_encrypted) VALUES($1,'read fixture','https://read.invalid','fixture-not-used')`, indexer)
	sql(`INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'read fixture','read-fixture','fixture-not-used',100)`, candidate, indexer)
	add := func(owner uuid.UUID, state string) uuid.UUID {
		op := uuid.New()
		sql(`INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id) VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), op, unit, root, owner.String())
		sql(`INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,content_path,progress,total_bytes,downloaded_bytes) VALUES($1,$2,$3,$4,$5,$6,'/owned-private/downloads','/owned-private/downloads/video.mkv',0.25,100,99)`, uuid.New(), op, client, candidate, strings.ReplaceAll(op.String(), "-", "")+"aaaaaaaa", state)
		return op
	}
	current, cancelOp, private := add(admin, "tracking"), add(admin, "unknown"), add(member, "unknown")
	// Shared demand must survive the explicit withdrawal as must the unknown task.
	sql(`INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id) VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), cancelOp, unit, root, member.String())
	plan := uuid.New()
	sql(`INSERT INTO transfer_plan(id,operation_id,root_id,state,strategy) VALUES($1,$2,$3,'created','hardlink')`, plan, current, root)
	for i := 0; i < 125; i++ {
		state := "pending"
		if i%3 == 1 {
			state = "running"
		}
		if i%3 == 2 {
			state = "failed"
		}
		name := fmt.Sprintf("episode-%03d-%s.mkv", i, strings.Repeat("long-readable-name-", 8))
		sql(`INSERT INTO transfer_step(id,plan_id,unit_id,source_path,target_path,source_rel,target_rel,action,state,step_order,source_size,source_mtime,error) VALUES($1,$2,$3,'/owned-private/source','/owned-private/target','private/source.mkv',$4,'copy',$5,$6,123,456,'private failure detail')`, uuid.New(), plan, unit, "private/"+name, state, i)
	}
	foreignPlan := uuid.New()
	sql(`INSERT INTO transfer_plan(id,operation_id,root_id,state,strategy) VALUES($1,$2,$3,'created','hardlink')`, foreignPlan, private, root)
	sql(`INSERT INTO transfer_step(id,plan_id,unit_id,source_path,target_path,source_rel,target_rel,action,state,step_order,source_size,source_mtime) VALUES($1,$2,$3,'/owned-private/source','/owned-private/target','private/source.mkv','private/FOREIGN-PRIVATE-FILENAME.mkv','copy','pending',0,1,1)`, uuid.New(), foreignPlan, unit)
	sample := func(mode string) error {
		ctx := t.Context()
		tx, e := f.app.DBPool().Begin(ctx)
		if e != nil {
			return e
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q := db.New(tx)
		c, e := q.LockDownloadClientForEdit(ctx, client)
		if e != nil {
			return e
		}
		_, e = q.GetDownloadTaskByOpID(ctx, current)
		if e != nil {
			return e
		}
		stamp, e := postgres.DownloadRateClientStamp(c)
		if e != nil {
			return e
		}
		value := int64(9223372036854775807)
		rate := &value
		if mode == "zero" {
			value = 0
		}
		if mode == "unknown" {
			rate = nil
		}
		if _, e = tx.Exec(ctx, `UPDATE download_task SET progress=CASE WHEN $2='zero' THEN 0.5 ELSE 0.25 END WHERE operation_id=$1`, current, mode); e != nil {
			return e
		}
		task, e := q.GetDownloadTaskByOpID(ctx, current)
		if e != nil {
			return e
		}
		if e = postgres.RecordDownloadRate(ctx, tx, task, stamp, rate, "ordinary"); e != nil {
			return e
		}
		if mode == "stale" {
			if _, e = tx.Exec(ctx, `UPDATE download_rate_observation SET observed_at=clock_timestamp()-interval '60 seconds' WHERE task_id=$1`, task.ID); e != nil {
				return e
			}
		}
		return tx.Commit(ctx)
	}
	if e := sample("large"); e != nil {
		t.Fatal(e)
	}
	tables := []string{"media", "media_unit", "download_client", "release_candidate", "download_requirement", "download_task", "transfer_plan", "transfer_step", "unit_reservation", "library_file", "library_file_unit", "idempotency_record", "audit_log", "download_rate_observation"}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			var raw string
			if e := f.app.DBPool().QueryRow(t.Context(), fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]')::text FROM %s x`, table)).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			out[table] = raw
		}
		for key, q := range map[string]string{"jobs": `SELECT coalesce(jsonb_agg(jsonb_build_array(id,kind,payload) ORDER BY id),'[]')::text FROM job`, "events": `SELECT coalesce(jsonb_agg(jsonb_build_array(id,event_type,payload) ORDER BY id),'[]')::text FROM outbox_event`} {
			var raw string
			if e := f.app.DBPool().QueryRow(t.Context(), q).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			out[key] = raw
		}
		return out
	}
	baseline := snapshot()
	originalTasks, originalReservations := baseline["download_task"], baseline["unit_reservation"]
	var apiReads, apiWrites atomic.Int64
	// App.Run legitimately publishes failed background observations while Chrome reads.
	// Preserve every binding and business row; only a real failed login may clear
	// nullable display values and advance the independent sample timestamp.
	sameTable := func(table, before, after string) bool {
		if before == after {
			return true
		}
		if table != "download_rate_observation" || native.Load() == 0 {
			return false
		}
		var oldRows, newRows []map[string]json.RawMessage
		if json.Unmarshal([]byte(before), &oldRows) != nil || json.Unmarshal([]byte(after), &newRows) != nil || len(oldRows) != len(newRows) || len(oldRows) == 0 {
			return false
		}
		for i, oldRow := range oldRows {
			newRow := newRows[i]
			if string(newRow["download_rate_bytes_per_second"]) != "null" || string(newRow["observed_progress"]) != "null" {
				return false
			}
			var oldTime, newTime time.Time
			if json.Unmarshal(oldRow["observed_at"], &oldTime) != nil || json.Unmarshal(newRow["observed_at"], &newTime) != nil || newTime.Before(oldTime) || newTime.After(time.Now().Add(time.Second)) {
				return false
			}
			for _, field := range []string{"download_rate_bytes_per_second", "observed_progress", "observed_at"} {
				delete(oldRow, field)
				delete(newRow, field)
			}
			if !reflect.DeepEqual(oldRow, newRow) {
				return false
			}
		}
		return true
	}
	matchesSnapshot := func(after map[string]string) bool {
		if len(after) != len(baseline) {
			return false
		}
		for table, before := range baseline {
			if !sameTable(table, before, after[table]) {
				return false
			}
		}
		return true
	}
	var mu sync.Mutex
	controls, commandChecks := 0, 0
	storageUnavailable := false
	defer func() {
		if storageUnavailable {
			_, e := f.app.DBPool().Exec(context.Background(), `ALTER TABLE owned_operation_rate_unavailable RENAME TO download_rate_observation`)
			if e != nil {
				t.Error(e)
			}
		}
	}()
	target, _ := url.Parse(f.base)
	proxy := &httputil.ReverseProxy{FlushInterval: -1, Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(target)
		p.Out.Host = target.Host
		if p.In.Header.Get("Origin") == "http://"+p.In.Host {
			p.Out.Header.Set("Origin", f.base)
		}
	}}
	files := http.FileServer(http.Dir(assets))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method == "GET" {
				apiReads.Add(1)
			} else {
				apiWrites.Add(1)
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__operation/") {
			mu.Lock()
			defer mu.Unlock()
			action := strings.TrimPrefix(r.URL.Path, "/__operation/")
			fail := func(e error) { t.Error(e); http.Error(w, "fixture checkpoint failed", 500) }
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			switch action {
			case "check":
				after := snapshot()
				if !matchesSnapshot(after) {
					for table, before := range baseline {
						if after[table] == before {
							continue
						}
						var oldRows, newRows []map[string]json.RawMessage
						var fields []string
						if json.Unmarshal([]byte(before), &oldRows) == nil && json.Unmarshal([]byte(after[table]), &newRows) == nil && len(oldRows) == len(newRows) {
							for i, row := range oldRows {
								for field, value := range row {
									if string(newRows[i][field]) != string(value) {
										fields = append(fields, field)
									}
								}
							}
						}
						t.Logf("M6_OPERATION_GRAPH_DIFF table=%s old_rows=%d new_rows=%d fields=%v background_native_login_attempts=%d", table, len(oldRows), len(newRows), fields, native.Load())
					}
					fail(fmt.Errorf("read phase changed protected business graph"))
					return
				}
			case "large", "zero", "unknown", "stale":
				if !matchesSnapshot(snapshot()) {
					fail(fmt.Errorf("untracked mutation before sample"))
					return
				}
				if e := sample(action); e != nil {
					fail(e)
					return
				}
				controls++
				baseline = snapshot()
			case "command":
				after := snapshot()
				for key, old := range baseline {
					if key == "download_requirement" || key == "audit_log" || key == "events" || key == "idempotency_record" {
						continue
					}
					if !sameTable(key, old, after[key]) {
						fail(fmt.Errorf("withdraw changed protected %s", key))
						return
					}
				}
				if m5InteractiveInt(t, f, `SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND actor_id=$2 AND state='released'`, cancelOp, admin.String()) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND actor_id=$2 AND state='pending'`, cancelOp, member.String()) != 1 {
					fail(fmt.Errorf("withdraw did not preserve shared demand"))
					return
				}
				if m5InteractiveInt(t, f, `SELECT count(*) FROM audit_log WHERE action='download.requirement_released'`) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM outbox_event WHERE event_type='download.requirement_released'`) != 1 {
					fail(fmt.Errorf("expected exactly one withdrawal audit/event"))
					return
				}
				commandChecks++
				baseline = after
			case "storage-down":
				if !matchesSnapshot(snapshot()) {
					fail(fmt.Errorf("untracked write before storage fault"))
					return
				}
				if _, e := f.app.DBPool().Exec(r.Context(), `ALTER TABLE download_rate_observation RENAME TO owned_operation_rate_unavailable`); e != nil {
					fail(e)
					return
				}
				storageUnavailable = true
				controls++
			case "storage-up":
				if _, e := f.app.DBPool().Exec(r.Context(), `ALTER TABLE owned_operation_rate_unavailable RENAME TO download_rate_observation`); e != nil {
					fail(e)
					return
				}
				storageUnavailable = false
				controls++
			case "background-observed":
				ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
				defer cancel()
				for {
					var cleared bool
					if e := f.app.DBPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM download_rate_observation r JOIN download_task d ON d.id=r.task_id WHERE d.operation_id=$1 AND r.download_rate_bytes_per_second IS NULL AND r.observed_progress IS NULL)`, current).Scan(&cleared); e != nil {
						fail(e)
						return
					}
					if native.Load() > 0 && cleared {
						break
					}
					select {
					case <-ctx.Done():
						fail(fmt.Errorf("background observation did not publish an unknown rate"))
						return
					case <-time.After(25 * time.Millisecond):
					}
				}
				t.Log("M6_OPERATION_BACKGROUND_OBSERVATION_VERIFIED rate_unknown=true progress_unknown=true bindings_and_business_rows_unchanged=true")
			case "demote", "restore":
				role := "member"
				if action == "restore" {
					role = "admin"
				}
				if _, e := f.app.DBPool().Exec(r.Context(), `UPDATE app_user SET role=$2 WHERE id=$1`, admin, role); e != nil {
					fail(e)
					return
				}
				controls++
			case "revoke":
				if _, e := f.app.DBPool().Exec(r.Context(), `UPDATE app_session SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, admin); e != nil {
					fail(e)
					return
				}
				controls++
			default:
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/theme.js" || strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(bridge.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 175*time.Second)
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
				t.Log("M6_OPERATION_CHROME_CLEANUP_VERIFIED")
			}
		})
	}
	defer stop()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/tests/operation-activity-browser.mjs"))
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M6_OPERATION_BASE="+bridge.URL, "PULSE_M6_OPERATION_SESSION="+f.session, "PULSE_M6_OPERATION_MEMBER_SESSION="+memberSession, "PULSE_M6_OPERATION_CDP="+cdp, "PULSE_M6_OPERATION_CURRENT="+current.String(), "PULSE_M6_OPERATION_CANCEL="+cancelOp.String(), "PULSE_M6_OPERATION_PRIVATE="+private.String())
	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, os.Stdout)
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	stop()
	if err != nil {
		t.Fatalf("actual Chrome: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "M6_OPERATION_ACTIVITY_BROWSER_PASS ") {
		t.Fatal("missing browser pass")
	}
	if !matchesSnapshot(snapshot()) || baseline["unit_reservation"] != originalReservations {
		t.Fatal("terminal business drift")
	}
	// Only the explicitly tracked progress fixture changes task display counters.
	if originalTasks == "" || apiReads.Load() < 25 || commandChecks != 1 {
		t.Fatal("missing read/command/native boundary evidence")
	}
	t.Logf("M6_OPERATION_APP_COOKIE_PG_PASS schema=93 background_native_login_attempts=%d GET_native_IO=covered_by_public_API Add=0 Remove=0 extra_file_IO=0 api_GET=%d api_non_GET=%d fixture_controls=%d command_checks=%d extra_business_writes=0 SQL_display_only=true genuine_verified_bytes=false", native.Load(), apiReads.Load(), apiWrites.Load(), controls, commandChecks)
	t.Log("M6_OPERATION_SCREENSHOTS_READY " + shots)
}
