package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
)

// Owned synthetic official v1.10.3 welcome header; intentionally no stats fields.
func m8CheckinRuntimeHTML(attended bool) string {
	mark := `<a href="attendance.php" class="faqlink">[Attend get bonus]</a>`
	if attended {
		mark = `<a href="attendance.php" class="">[Attend got: 0, card: 0]</a>`
	}
	return `<!doctype html><html><body><table id="info_block"><tr><td><table><tr><td class="bottom"><span class="medium">Welcome <a href="userdetails.php?id=42"><b>Alice</b></a> [<a href="logout.php">Logout</a>] ` + mark + `</span></td></tr></table></td></tr></table><a href="userdetails.php?id=999">Other user</a></body></html>`
}

type m8CheckinRuntimeFixture struct {
	*m7ScrapeFixture
	ctx                     context.Context
	runtime                 *m8SubtitleRuntimeFixture
	repo                    *postgres.SiteCheckinStore
	source                  ports.IndexerManagementView
	base                    string
	gets, posts, unexpected atomic.Int32
	firstGET                atomic.Int64
	postedProof             atomic.Value
	attended                atomic.Bool
	mode                    string
	entered, release        chan struct{}
	enterOnce, releaseOnce  sync.Once
	protected               string
}

func m8NewCheckinRuntime(t *testing.T, mode string) *m8CheckinRuntimeFixture {
	t.Helper()
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	f := m7NewScrapeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Second)
	t.Cleanup(cancel)
	v := &m8CheckinRuntimeFixture{m7ScrapeFixture: f, ctx: ctx, mode: mode, entered: make(chan struct{}), release: make(chan struct{})}
	cfg := *f.app.Config()
	// Auth App owns its lock. Ensure the actual runtime root's key BEFORE encryption.
	cfg.DataDir = t.TempDir()
	key, err := crypto.EnsureMasterKey(cfg.DataDir, cfg.SecretKey)
	if err != nil {
		t.Fatal("runtime master key initialization failed")
	}
	secrets := crypto.NewSecretProtector(key)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "owned-checkin-fixture=synthetic" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" || r.ProtoMajor != 1 {
			v.unexpected.Add(1)
			w.WriteHeader(400)
			return
		}
		if (r.Method != "GET" || r.URL.Path != "/pt/index.php") && (r.Method != "POST" || r.URL.Path != "/pt/attendance.php") {
			v.unexpected.Add(1)
			w.WriteHeader(400)
			return
		}
		// Separate pool observes committed reservation, current queue fencing, and physical execution.
		if !v.predicate(`SELECT EXISTS(SELECT 1 FROM site_checkin_execution e JOIN site_checkin_day d ON d.id=e.day_id JOIN job j ON j.id=e.job_id JOIN indexer i ON i.id=d.indexer_id WHERE i.id=$1 AND e.finished_at IS NULL AND e.observe_started_at IS NOT NULL AND i.next_query_at>clock_timestamp() AND j.state='running' AND j.lease_owner=e.owner AND j.lease_token=e.token AND j.lease_until>clock_timestamp())`, v.source.ID) {
			t.Error("native request without committed budget and live fenced execution")
			w.WriteHeader(500)
			return
		}
		if r.Method == "GET" {
			v.gets.Add(1)
			v.firstGET.CompareAndSwap(0, time.Now().UnixNano())
		} else {
			v.posts.Add(1)
			if time.Since(time.Unix(0, v.firstGET.Load())) < 1900*time.Millisecond {
				t.Error("POST did not wait for default two-second shared budget")
				w.WriteHeader(500)
				return
			}
			if !v.predicate(`SELECT EXISTS(SELECT 1 FROM site_checkin_day d JOIN site_checkin_execution e ON e.id=d.prepared_execution_id WHERE d.indexer_id=$1 AND d.status='prepared' AND e.mode='send' AND e.state='prepared' AND e.permission_id IS NOT NULL AND e.prepared_at IS NOT NULL AND e.valid_until>clock_timestamp())`, v.source.ID) {
				t.Error("POST preceded committed send permission")
				w.WriteHeader(500)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 2))
			if err != nil || len(body) != 0 {
				t.Error("native POST body must be empty")
				w.WriteHeader(400)
				return
			}
			var committed string
			if v.pool.QueryRow(v.ctx, `SELECT d.prepared_execution_id::text||':'||e.permission_id::text FROM site_checkin_day d JOIN site_checkin_execution e ON e.id=d.prepared_execution_id WHERE d.indexer_id=$1`, v.source.ID).Scan(&committed) != nil {
				t.Error("request-time committed permission unavailable")
				w.WriteHeader(500)
				return
			}
			v.postedProof.Store(committed)
			v.enterOnce.Do(func() { close(v.entered) })
			if mode == "disable" {
				select {
				case <-v.release:
				case <-r.Context().Done():
					return
				case <-time.After(15 * time.Second):
					t.Error("blocked POST deadline")
					return
				}
			}
			v.attended.Store(true)
			if mode == "lost" {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error("owned HTTP1 hijack failed")
					return
				}
				_ = conn.Close()
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, m8CheckinRuntimeHTML(v.attended.Load()))
	}))
	t.Cleanup(remote.Close)
	manager := search.NewIndexerManagementService(postgres.NewIndexerManagementStore(f.pool, f.app.IDGen(), secrets), secrets)
	actor, err := f.auth.AuthenticateToken(ctx, f.cookie.Value)
	if err != nil {
		t.Fatal("fixture authentication failed")
	}
	sourceURL, cookie := remote.URL+"/pt/", "owned-checkin-fixture=synthetic"
	receipt, err := manager.Create(ctx, actor, ports.IndexerManagementInput{Name: "Owned runtime checkin", Protocol: ports.IndexerProtocolNexusPHP, URL: &sourceURL, Credential: &cookie, Categories: []int{}}, "checkin-runtime-source")
	if err != nil {
		t.Fatal("encrypted native source creation failed")
	}
	v.source = receipt.Indexer
	// SQL prepares only independent health/RSS schedules, never checkin config/evidence.
	v.sql(t, `UPDATE indexer SET rss_enabled=false,health_next_check_at=clock_timestamp()+interval '1 day' WHERE id=$1`, v.source.ID)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("owned runtime listener failed")
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	if listener.Close() != nil {
		t.Fatal("owned listener close failed")
	}
	v.base = "http://" + cfg.Server.HTTPAddr
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	t.Cleanup(tr.CloseIdleConnections)
	// Native client requires the standard transport; no RoundTripper override.
	v.runtime = &m8SubtitleRuntimeFixture{pool: f.pool, cfg: &cfg, client: &http.Client{Transport: tr, Timeout: 15 * time.Second}}
	v.repo = postgres.NewSiteCheckinStore(f.pool, f.app.IDGen(), secrets)
	h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetSiteCheckinConfigService(site.NewCheckinConfigService(postgres.NewSiteCheckinConfigStore(f.pool, f.app.IDGen(), secrets)))
	h.SetSiteCheckinHistoryService(site.NewCheckinHistoryReader(postgres.NewSiteCheckinHistoryStore(f.pool)))
	v.router = httpapi.NewRouter(f.app, h, f.auth, nil)
	// Registered last: unblock and join before server/pool/key directory cleanup.
	t.Cleanup(func() { v.unblock(); v.stop(t) })
	// Establish the ordinary native root/scan baseline before any checkin config
	// is saved. Every protected table remains in the later full comparison.
	v.start(t)
	v.wait(t, "initial native library scan baseline", func() bool {
		return v.predicate(`SELECT (SELECT count(*)>=2 AND bool_and(baseline_completed) FROM library_root)
		 AND NOT EXISTS(SELECT 1 FROM library_scan_job s JOIN job j ON j.id=s.job_id WHERE j.state<>'succeeded')
		 AND NOT EXISTS(SELECT 1 FROM library_scan_schedule WHERE next_run_at<=clock_timestamp())`)
	})
	v.stop(t)
	v.protected = v.graph(t)
	return v
}
func (v *m8CheckinRuntimeFixture) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, e := v.pool.Exec(v.ctx, q, args...); e != nil {
		t.Fatal("owned SQL fixture operation failed")
	}
}
func (v *m8CheckinRuntimeFixture) predicate(q string, args ...any) bool {
	var b bool
	return v.pool.QueryRow(v.ctx, q, args...).Scan(&b) == nil && b
}
func (v *m8CheckinRuntimeFixture) wait(t *testing.T, phase string, fn func() bool) {
	t.Helper()
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-v.ctx.Done():
			t.Fatalf("checkin runtime deadline: %s", phase)
		case <-timer.C:
		}
	}
}
func (v *m8CheckinRuntimeFixture) unblock() { v.releaseOnce.Do(func() { close(v.release) }) }
func (v *m8CheckinRuntimeFixture) stop(t *testing.T) {
	t.Helper()
	if v.runtime.cancel == nil {
		return
	}
	v.runtime.cancel()
	select {
	case err := <-v.runtime.done:
		if err != nil {
			t.Error("App.Run returned failure")
		}
	case <-time.After(46 * time.Second):
		t.Error("App shutdown exceeded HTTP15s + worker30s")
		return
	}
	if v.runtime.app.Close() != nil {
		t.Error("runtime close failed")
	}
	v.runtime.cancel = nil
}
func (v *m8CheckinRuntimeFixture) call(t *testing.T, method, path, body, key string) *http.Response {
	t.Helper()
	req, e := http.NewRequestWithContext(v.ctx, method, v.base+path, strings.NewReader(body))
	if e != nil {
		t.Fatal("HTTP request construction failed")
	}
	req.Header.Set("Origin", v.base)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(v.cookie)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, e := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if e != nil {
		t.Fatal("actual App HTTP request failed")
	}
	return res
}
func (v *m8CheckinRuntimeFixture) start(t *testing.T) {
	t.Helper()
	v.runtime.start(t)
	v.wait(t, "actual HTTP /readyz", func() bool {
		req, _ := http.NewRequestWithContext(v.ctx, "GET", v.base+"/readyz", nil)
		res, e := (&http.Client{Timeout: time.Second}).Do(req)
		if e != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == 200
	})
	// This is an actual TCP CookieLogin response from the running App.
	res := v.call(t, "POST", "/api/v1/auth/login", `{"username":"scrape-admin","password":"OwnedScrapeFixture123!"}`, "")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("runtime CookieLogin status=%d", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == "pulsemedia_session" && c.Value != "" {
			v.cookie = c
			return
		}
	}
	t.Fatal("actual runtime CookieLogin did not issue session")
}
func (v *m8CheckinRuntimeFixture) path() string {
	return "/api/v1/indexers/" + v.source.ID.String() + "/checkin/config"
}
func (v *m8CheckinRuntimeFixture) save(t *testing.T, version int, enabled bool) {
	t.Helper()
	body := fmt.Sprintf(`{"expected_account_id":"42","profile":"nexusphp_1103_attendance_v1","timezone":"UTC","daily_at":"00:00","enabled":%t,"expected_version":%d,"expected_source_version":1}`, enabled, version)
	res := v.call(t, "PUT", v.path(), body, fmt.Sprintf("runtime-config-%d", version))
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("public checkin config save status=%d", res.StatusCode)
	}
	var r ports.SiteCheckinConfigReceipt
	if json.NewDecoder(res.Body).Decode(&r) != nil || r.Config.Version != int64(version+1) || r.Config.Enabled != enabled || r.Config.ExpectedAccountID != "42" || r.Config.Timezone != "UTC" || r.Config.DailyAt != "00:00" || r.Config.SourceConfigVersion != 1 {
		t.Fatal("public settings receipt mismatch")
	}
}
func (v *m8CheckinRuntimeFixture) settings(t *testing.T, version int64, enabled bool) {
	t.Helper()
	res := v.call(t, "GET", v.path(), "", "")
	defer func() { _ = res.Body.Close() }()
	var r struct {
		Config *ports.SiteCheckinConfig `json:"config"`
	}
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&r) != nil || r.Config == nil || r.Config.Version != version || r.Config.Enabled != enabled || r.Config.ExpectedAccountID != "42" || r.Config.Profile != ports.NexusPHP1103CheckinProfile || r.Config.Timezone != "UTC" || r.Config.DailyAt != "00:00" || r.Config.SourceConfigVersion != 1 {
		t.Fatal("public current settings changed unexpectedly")
	}
}
func (v *m8CheckinRuntimeFixture) graph(t *testing.T) string {
	// Intentional config commands/audits/events, auth sessions, indexer budgets and
	// checkin jobs/day/executions are outside this protected business graph.
	rows, err := v.pool.Query(v.ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename ~ '^(media|library|download|unit|acquisition|subscription|wanted)|ingestion|file_fact' ORDER BY tablename`)
	if err != nil {
		t.Fatal("protected graph table discovery failed")
	}
	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			rows.Close()
			t.Fatal("protected graph scan failed")
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal("protected graph discovery failed")
	}
	return m8ConfigSnapshot(t, v.m7ScrapeFixture, tables...)
}
func (v *m8CheckinRuntimeFixture) assert(t *testing.T, q string, args ...any) {
	t.Helper()
	if !v.predicate(q, args...) {
		t.Fatal("checkin database contract assertion failed")
	}
}
func (v *m8CheckinRuntimeFixture) proof(t *testing.T, gets, posts int32) {
	t.Helper()
	v.assert(t, `SELECT count(*)=1 AND bool_and(expected_account_id='42' AND profile='nexusphp_1103_attendance_v1' AND timezone='UTC' AND calendar_day=(clock_timestamp() AT TIME ZONE 'UTC')::date AND day_start=calendar_day::timestamp AT TIME ZONE 'UTC' AND day_end=day_start+interval '1 day') FROM site_checkin_day WHERE indexer_id=$1`, v.source.ID)
	v.assert(t, `SELECT count(*)=1 FROM site_checkin_execution e JOIN site_checkin_day d ON d.id=e.day_id WHERE d.indexer_id=$1 AND e.mode='send' AND e.prepared_at IS NOT NULL AND e.permission_id IS NOT NULL AND d.prepared_execution_id=e.id`, v.source.ID)
	v.assert(t, `SELECT count(*)=1 FROM job j JOIN site_checkin_day d ON d.original_job_id=j.id WHERE d.indexer_id=$1 AND j.kind='site_checkin'`, v.source.ID)
	v.assert(t, `SELECT count(*)=1 FROM site_checkin_execution e JOIN site_checkin_day d ON d.id=e.day_id WHERE d.indexer_id=$1 AND e.mode='send'`, v.source.ID)
	v.assert(t, `SELECT count(*)<=1 FROM site_checkin_execution e JOIN site_checkin_day d ON d.id=e.day_id WHERE d.indexer_id=$1 AND e.mode='observe'`, v.source.ID)
	v.assert(t, `SELECT count(*)<=1 FROM job j JOIN site_checkin_day d ON d.observation_job_id=j.id WHERE d.indexer_id=$1 AND j.kind='site_checkin_observe' AND j.max_attempts=1`, v.source.ID)
	if v.gets.Load() != gets || v.posts.Load() != posts || v.unexpected.Load() != 0 {
		t.Fatalf("owned native request counts GET=%d POST=%d unexpected=%d", v.gets.Load(), v.posts.Load(), v.unexpected.Load())
	}
	if after := v.graph(t); v.protected != after {
		t.Fatal("protected business graph changed: " + m8CheckinGraphChanges(v.protected, after))
	}
	v.assert(t, `SELECT NOT EXISTS(SELECT 1 FROM job WHERE kind IN ('site_account_stats_collect','indexer_health','search_endpoint','rss_poll'))`)
}
func (v *m8CheckinRuntimeFixture) frozen(t *testing.T) string {
	t.Helper()
	var s string
	if v.pool.QueryRow(v.ctx, `SELECT d.prepared_execution_id::text||':'||e.permission_id::text FROM site_checkin_day d JOIN site_checkin_execution e ON e.id=d.prepared_execution_id WHERE d.indexer_id=$1`, v.source.ID).Scan(&s) != nil {
		t.Fatal("missing immutable send proof")
	}
	return s
}
func (v *m8CheckinRuntimeFixture) repeat(t *testing.T, gets, posts int32) {
	t.Helper()
	before := v.frozen(t)
	v.stop(t)
	for range 2 {
		if v.repo.RecoverExpired(v.ctx) != nil || v.repo.EnqueueDue(v.ctx) != nil {
			t.Fatal("same day recovery/schedule failed")
		}
	}
	v.start(t)
	// Allow at least one full 3-second worker maintenance cycle.
	timer := time.NewTimer(3200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-v.ctx.Done():
		t.Fatal("restart observation deadline")
	}
	v.stop(t)
	if before != v.frozen(t) {
		t.Fatal("prepared pointer/permission changed")
	}
	v.proof(t, gets, posts)
	if sent := v.postedProof.Load(); sent != nil && sent.(string) != v.frozen(t) {
		t.Fatal("request-time prepared pointer/permission changed")
	}
}
func TestM8SiteCheckinRuntimePositivePG(t *testing.T) {
	v := m8NewCheckinRuntime(t, "positive")
	v.start(t)
	v.save(t, 0, true)
	v.wait(t, "positive native callback", func() bool {
		return v.predicate(`SELECT EXISTS(SELECT 1 FROM site_checkin_day WHERE indexer_id=$1 AND status='attended_observed')`, v.source.ID)
	})
	v.assert(t, `SELECT EXISTS(SELECT 1 FROM site_checkin_execution e JOIN site_checkin_day d ON d.prepared_execution_id=e.id JOIN job j ON j.id=d.original_job_id WHERE d.indexer_id=$1 AND e.state='attended_observed' AND e.result='{"account_id":"42","mark":"attended"}'::jsonb AND j.state='succeeded' AND d.observation_job_id IS NULL)`, v.source.ID)
	v.settings(t, 1, true)
	v.repeat(t, 1, 1)
	v.historyReadOnly(t)
	t.Log("M8_CHECKIN_RUNTIME_POSITIVE_PASS GET=1 POST=1 attendance_GET=0 day=1 prepared=1 attribution=false real_App=true real_Cookie=true")
}
func TestM8SiteCheckinRuntimeLostReceiptPG(t *testing.T) {
	v := m8NewCheckinRuntime(t, "lost")
	v.start(t)
	v.save(t, 0, true)
	v.wait(t, "lost response recovery observation", func() bool {
		return v.predicate(`SELECT EXISTS(SELECT 1 FROM site_checkin_day d JOIN site_checkin_execution e ON e.day_id=d.id AND e.job_id=d.observation_job_id JOIN job j ON j.id=d.original_job_id WHERE d.indexer_id=$1 AND d.status='attended_observed' AND e.mode='observe' AND e.state='attended_observed' AND j.state='dead')`, v.source.ID)
	})
	v.assert(t, `SELECT count(*)=1 AND bool_and(j.max_attempts=1 AND j.state='succeeded') FROM job j JOIN site_checkin_day d ON d.observation_job_id=j.id WHERE d.indexer_id=$1`, v.source.ID)
	v.assert(t, `SELECT EXISTS(SELECT 1 FROM site_checkin_execution e JOIN site_checkin_day d ON d.prepared_execution_id=e.id WHERE d.indexer_id=$1 AND e.state='review_required' AND e.result='{}'::jsonb)`, v.source.ID)
	v.settings(t, 1, true)
	v.repeat(t, 2, 1)
	t.Log("M8_CHECKIN_RUNTIME_LOST_RECEIPT_PASS TCP_POST=1 GET=2 attendance_GET=0 observation_jobs=1 immutable_permission=true forced_lease_expiry=false")
}
func TestM8SiteCheckinRuntimePreparedRestartPG(t *testing.T) {
	v := m8NewCheckinRuntime(t, "prepared")
	// Public Cookie PUT over owned TCP before workers start, using the exact runtime key.
	// The actual App.Run and actual runtime CookieLogin follow committed preparation.
	configHTTP := httptest.NewServer(v.router)
	t.Cleanup(configHTTP.Close)
	runtimeBase := v.base
	v.base = configHTTP.URL
	v.save(t, 0, true)
	v.base = runtimeBase
	configHTTP.Close()
	if v.repo.EnqueueDue(v.ctx) != nil {
		t.Fatal("dedicated day enqueue failed")
	}
	j, e := db.New(v.pool).ClaimJob(v.ctx, db.ClaimJobParams{Column1: []string{ports.SiteCheckinJobKind}, Column2: "owned-prepared-crash", Column3: 120000})
	if e != nil {
		t.Fatal("dedicated original queue lease unavailable")
	}
	lease := ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}
	p, e := v.repo.Acquire(v.ctx, lease)
	if e != nil {
		t.Fatal("prepared fixture acquire failed")
	}
	defer p.Close()
	if _, e = p.ReserveObserve(v.ctx); e != nil {
		t.Fatal("prepared fixture observe reservation failed")
	}
	native, e := (&siteCheckinClientBuilder{httpClient: v.runtime.client, clock: v.app.Clock()}).NewSiteCheckinClient(p.Snapshot())
	if e != nil {
		t.Fatal("native prepared client failed")
	}
	observation, e := native.ObserveCheckin(v.ctx, "42")
	if e != nil || observation.AccountID != "42" || observation.Mark != ports.CheckinMarkPending {
		t.Fatal("real prepared preflight must be pending")
	}
	// Controlled SQL fixture advances only the shared budget, never evidence or guards.
	v.sql(t, `UPDATE indexer SET next_query_at=clock_timestamp()-interval '1 second' WHERE id=$1`, v.source.ID)
	if _, e = p.PrepareSubmit(v.ctx, observation); e != nil {
		t.Fatal("committed prepare failed")
	}
	p.Close()
	frozen := v.frozen(t)
	// Crash simulation only: expire the original lease, retain permission and day.
	v.sql(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID)
	v.start(t)
	v.wait(t, "pending recovery review", func() bool {
		return v.predicate(`SELECT EXISTS(SELECT 1 FROM site_checkin_day d JOIN site_checkin_execution e ON e.job_id=d.observation_job_id JOIN job j ON j.id=e.job_id WHERE d.indexer_id=$1 AND d.status='review_required' AND e.mode='observe' AND e.state='review_required' AND e.finished_at IS NOT NULL AND j.state='dead')`, v.source.ID)
	})
	if v.attended.Load() || frozen != v.frozen(t) {
		t.Fatal("crash manufactured attendance or replaced permission")
	}
	v.settings(t, 1, true)
	v.repeat(t, 2, 0)
	t.Log("M8_CHECKIN_RUNTIME_PREPARED_RESTART_PASS fixture_preflight_GET=1 automatic_observation_GET=1 POST=0 pending_review=true observation_jobs=1")
}
func TestM8SiteCheckinRuntimeDisableLatePG(t *testing.T) {
	v := m8NewCheckinRuntime(t, "disable")
	v.start(t)
	v.save(t, 0, true)
	select {
	case <-v.entered:
	case <-v.ctx.Done():
		t.Fatal("native POST entry deadline")
	}
	frozen := v.frozen(t)
	v.save(t, 1, false)
	v.assert(t, `SELECT EXISTS(SELECT 1 FROM site_checkin_execution e JOIN site_checkin_day d ON d.prepared_execution_id=e.id WHERE d.indexer_id=$1 AND e.finished_at IS NULL)`, v.source.ID)
	v.unblock()
	v.wait(t, "disabled late callback refused", func() bool {
		return v.predicate(`SELECT EXISTS(SELECT 1 FROM site_checkin_day d JOIN job j ON j.id=d.original_job_id WHERE d.indexer_id=$1 AND j.state='dead' AND d.status IN ('prepared','review_required') AND d.observed_at IS NULL)`, v.source.ID)
	})
	if frozen != v.frozen(t) {
		t.Fatal("disable replaced original permission")
	}
	v.settings(t, 2, false)
	v.repeat(t, 1, 1)
	// A new enabled config version cannot grant a second same-day send.
	v.start(t)
	v.save(t, 2, true)
	v.settings(t, 3, true)
	v.repeat(t, 1, 1)
	v.assert(t, `SELECT NOT EXISTS(SELECT 1 FROM site_checkin_day WHERE status='attended_observed') AND NOT EXISTS(SELECT 1 FROM site_checkin_execution WHERE state='attended_observed')`)
	t.Log("M8_CHECKIN_RUNTIME_DISABLE_LATE_PASS POST=1 GET=1 late_positive_rejected=true permission_retained=true new_config_no_resend=true config_audits_intentional=true")
}

func (v *m8CheckinRuntimeFixture) historyReadOnly(t *testing.T) {
	t.Helper()
	rows, err := v.pool.Query(v.ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename<>'app_session' ORDER BY tablename`)
	if err != nil {
		t.Fatal("history snapshot table discovery failed")
	}
	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			rows.Close()
			t.Fatal("history snapshot table scan failed")
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal("history snapshot table iteration failed")
	}
	before := m8ConfigSnapshot(t, v.m7ScrapeFixture, tables...)
	gets, posts := v.gets.Load(), v.posts.Load()
	server := httptest.NewServer(v.router)
	defer server.Close()
	req, err := http.NewRequestWithContext(v.ctx, "GET", server.URL+"/api/v1/indexers/"+v.source.ID.String()+"/checkin/history?limit=1", nil)
	if err != nil {
		t.Fatal("history request construction failed")
	}
	req.AddCookie(v.cookie)
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("history request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("history status or cache policy failed", res.StatusCode)
	}
	var page httpapi.SiteCheckinHistory
	if json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&page) != nil || page.Config == nil || len(page.Rows) != 1 || page.NextCursor != nil {
		t.Fatal("history page incomplete")
	}
	day := page.Rows[0]
	if day.ExpectedAccountId != "42" || day.Timezone != "UTC" || !day.Prepared || string(day.State) != "attended_observed" || day.ObservedAt == nil || day.RestoreAcknowledged {
		t.Fatal("history lost the frozen daily result")
	}
	if after := m8ConfigSnapshot(t, v.m7ScrapeFixture, tables...); before != after {
		t.Fatal("history GET changed persisted data: " + m8CheckinGraphChanges(before, after))
	}
	if v.gets.Load() != gets || v.posts.Load() != posts {
		t.Fatal("history GET triggered native network I/O")
	}
	t.Log("M8_CHECKIN_HISTORY_PUBLIC_PASS real_Cookie=true after_App_stop=true native_requests=0 all_public_tables_unchanged=true auth_session_touch_excluded=true")
}

// Report structure only: no row values, credentials, URLs or private evidence.
func m8CheckinGraphChanges(before, after string) string {
	decode := func(raw string) map[string][]map[string]json.RawMessage {
		out := make(map[string][]map[string]json.RawMessage)
		for raw != "" {
			index := strings.IndexByte(raw, '[')
			if index < 0 {
				return nil
			}
			name := raw[:index]
			decoder := json.NewDecoder(strings.NewReader(raw[index:]))
			var rows []map[string]json.RawMessage
			if decoder.Decode(&rows) != nil {
				return nil
			}
			out[name] = rows
			raw = raw[index+int(decoder.InputOffset()):]
		}
		return out
	}
	old, current := decode(before), decode(after)
	if old == nil || current == nil {
		return "snapshot structure unavailable"
	}
	var changes []string
	for name, prior := range old {
		rows := current[name]
		oldJSON, _ := json.Marshal(prior)
		newJSON, _ := json.Marshal(rows)
		if string(oldJSON) == string(newJSON) {
			continue
		}
		fields := make(map[string]bool)
		for i, row := range rows {
			for key, value := range row {
				if i >= len(prior) || string(prior[i][key]) != string(value) {
					fields[key] = true
				}
			}
		}
		var names []string
		for key := range fields {
			names = append(names, key)
		}
		sort.Strings(names)
		changes = append(changes, fmt.Sprintf("%s rows=%d->%d fields=%s", name, len(prior), len(rows), strings.Join(names, ",")))
	}
	sort.Strings(changes)
	return strings.Join(changes, "; ")
}
