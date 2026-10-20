package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
)

// Synthetic official Classic info_block structure, matching the native parser's
// fixture (xiaomlove/nexusphp a3ba82be64e7c1cbfa72230d6b0dfb35dd3df459).
// No private PT, search command, torrent download, or downloader Add is involved.
const m8StatsRuntimeHTML = `<!doctype html><html><body><table id="info_block"><tr><td><table><tr>
<td class="bottom"><span class="medium">Welcome back,
<a href="userdetails.php?id=42"><b>Alice</b></a> [<a href="logout.php">Logout</a>]
<font class="color_bonus">Bonus</font>[<a href="mybonus.php">Use</a>]: 1,234.5
<font class="color_invite">Invites</font>[<a href="invite.php?id=42">Send</a>]: 9<br>
<font class="color_ratio">Ratio</font> <font color="green">2.000</font>
<font class="color_uploaded">Uploaded</font> 2.00 GB
<font class="color_downloaded">Downloaded</font> 1.00 GB
<font class="color_active">Active</font> 12
<font class="color_connectable">Connectable</font> Yes</span></td>
<td class="bottom">Time: 12:00</td></tr></table></td></tr></table>
<a href="userdetails.php?id=999">Other user</a><div>Bonus per hour: 9999.9</div></body></html>`

// Preserve the actual native HTTP client; restrict its transport to owned HTTP.
type m8StatsLocalTransport struct {
	host      string
	transport http.RoundTripper
}

func (r m8StatsLocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Host != r.host {
		return nil, errors.New("unexpected account statistics destination")
	}
	return r.transport.RoundTrip(req)
}

type m8StatsRuntimeFixture struct {
	*m7ScrapeFixture
	ctx                                context.Context
	runtime                            *m8SubtitleRuntimeFixture
	source                             ports.IndexerManagementView
	base                               string
	statsCalls, probeCalls, unexpected atomic.Int32
	mode                               atomic.Int32
	entered, release                   chan struct{}
	enteredOnce, releaseOnce           sync.Once
}

func m8NewStatsRuntimeFixture(t *testing.T, blocked bool) *m8StatsRuntimeFixture {
	t.Helper()
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	f := m7NewScrapeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	t.Cleanup(cancel)
	// The authentication fixture already owns its App directory lock. The runtime
	// gets a separate owned directory; encrypt source credentials with its exact key.
	cfgCopy := *f.app.Config()
	cfgCopy.DataDir = t.TempDir()
	v := &m8StatsRuntimeFixture{m7ScrapeFixture: f, ctx: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	if blocked {
		v.mode.Store(-1)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Cookie") != "owned-account-fixture=synthetic" || r.Header.Get("Authorization") != "" {
			v.unexpected.Add(1)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/pt/torrents.php":
			v.probeCalls.Add(1)
			// A legitimate empty result for the independently scheduled health probe.
			_, _ = io.WriteString(w, `<!doctype html><html><body><table class="torrents"><tr><td>No torrents found</td></tr></table></body></html>`)
		case "/pt/index.php":
			v.statsCalls.Add(1)
			var reserved bool
			err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM site_account_stats_attempt a JOIN job j ON j.id=a.job_id JOIN indexer i ON i.id=a.indexer_id WHERE a.indexer_id=$1 AND a.state='running' AND a.started_at IS NOT NULL AND j.state='running' AND j.lease_owner=a.execution_owner AND j.lease_token=a.execution_token AND j.lease_until>clock_timestamp() AND i.next_query_at> a.started_at)`, v.source.ID).Scan(&reserved)
			if err != nil || !reserved {
				t.Error("native request preceded successful budget reservation and fenced attempt")
				w.WriteHeader(500)
				return
			}
			mode := v.mode.Load()
			if mode == -1 {
				v.enteredOnce.Do(func() { close(v.entered) })
				select {
				case <-v.release:
				case <-r.Context().Done():
					return
				}
			}
			if mode == 429 {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(429)
				_, _ = io.WriteString(w, "owned-private-response-body")
				return
			}
			if mode == 403 {
				w.WriteHeader(403)
				_, _ = io.WriteString(w, "owned-private-response-body")
				return
			}
			_, _ = io.WriteString(w, m8StatsRuntimeHTML)
		default:
			v.unexpected.Add(1)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(remote.Close)
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	target, err := url.Parse(remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Same directory and key that this actual App.Run will load, never fake SQL ciphertext.
	key, err := crypto.EnsureMasterKey(cfgCopy.DataDir, cfgCopy.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	protector := crypto.NewSecretProtector(key)
	manager := search.NewIndexerManagementService(postgres.NewIndexerManagementStore(f.pool, f.app.IDGen(), protector), protector)
	actor, err := f.auth.AuthenticateToken(ctx, f.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	sourceURL, credential := remote.URL+"/pt/", "owned-account-fixture=synthetic"
	receipt, err := manager.Create(ctx, actor, ports.IndexerManagementInput{Name: "Owned runtime account", Protocol: ports.IndexerProtocolNexusPHP, URL: &sourceURL, Credential: &credential, Categories: []int{}}, "stats-runtime-source")
	if err != nil {
		t.Fatal("create actual source", err)
	}
	v.source = receipt.Indexer
	h := httpapi.NewServerHandler(f.app, f.auth, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetSiteAccountStatsConfigService(site.NewAccountStatsConfigService(postgres.NewSiteAccountStatsConfigStore(f.pool, f.app.IDGen(), protector)))
	h.SetSiteAccountStatsService(site.NewAccountStatsReader(postgres.NewSiteAccountStatsStore(f.pool, f.app.IDGen(), protector)))
	f.router = httpapi.NewRouter(f.app, h, f.auth, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfgCopy.Server.HTTPAddr = listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	v.base = "http://" + cfgCopy.Server.HTTPAddr
	v.runtime = &m8SubtitleRuntimeFixture{pool: f.pool, cfg: &cfgCopy, client: &http.Client{Transport: m8StatsLocalTransport{target.Host, tr}, Timeout: 15 * time.Second}}
	// Registered last: always unblock, cancel and join before HTTP/pool cleanup.
	t.Cleanup(func() { v.releaseOnce.Do(func() { close(v.release) }); v.stop(t) })
	return v
}
func (v *m8StatsRuntimeFixture) path() string {
	return "/api/v1/indexers/" + v.source.ID.String() + "/account-stats"
}
func (v *m8StatsRuntimeFixture) body(version int64, enabled bool) string {
	return fmt.Sprintf(`{"expected_account_id":"42","profile":"nexusphp_classic_info_block_v1","enabled":%t,"interval_seconds":900,"expected_version":%d,"expected_source_version":%d}`, enabled, version, v.source.ConfigVersion)
}
func (v *m8StatsRuntimeFixture) wait(t *testing.T, phase string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-v.ctx.Done():
			t.Fatalf("account statistics acceptance deadline: %s", phase)
		case <-ticker.C:
		}
	}
}
func (v *m8StatsRuntimeFixture) predicate(query string, args ...any) bool {
	var ok bool
	return v.pool.QueryRow(v.ctx, query, args...).Scan(&ok) == nil && ok
}
func (v *m8StatsRuntimeFixture) start(t *testing.T) {
	t.Helper()
	v.runtime.start(t)
	v.wait(t, "actual HTTP readiness", func() bool {
		req, _ := http.NewRequestWithContext(v.ctx, "GET", v.base+"/api/v1/auth/status", nil)
		res, err := (&http.Client{Timeout: time.Second}).Do(req)
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == 200
	})
}
func (v *m8StatsRuntimeFixture) stop(t *testing.T) {
	t.Helper()
	if v.runtime.cancel == nil {
		return
	}
	v.runtime.cancel()
	select {
	case err := <-v.runtime.done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(50 * time.Second):
		t.Error("App.Run failed HTTP15s + worker30s shutdown budget")
		return
	}
	if err := v.runtime.app.Close(); err != nil {
		t.Error(err)
	}
	v.runtime.cancel = nil
}
func (v *m8StatsRuntimeFixture) requestHTTP(t *testing.T, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequestWithContext(v.ctx, method, v.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", v.base)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(v.cookie)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("actual App HTTP request", err)
	}
	defer func() { _ = res.Body.Close() }()
	w := httptest.NewRecorder()
	w.Code = res.StatusCode
	w.Header().Set("Cache-Control", res.Header.Get("Cache-Control"))
	_, err = io.Copy(w.Body, res.Body)
	if err != nil {
		t.Fatal(err)
	}
	m8ConfigCode(t, w, 200)
	v.noSecrets(t, w.Body.String())
	return w
}
func (v *m8StatsRuntimeFixture) noSecrets(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"owned-account-fixture", "owned-private-response-body", "<table", "api_key_encrypted", "http://"} {
		if strings.Contains(body, secret) {
			t.Fatal("statistics response exposed execution material")
		}
	}
}
func (v *m8StatsRuntimeFixture) view(t *testing.T, w *httptest.ResponseRecorder) *ports.SiteAccountStatsView {
	t.Helper()
	m8ConfigCode(t, w, 200)
	v.noSecrets(t, w.Body.String())
	var out struct {
		Observation *ports.SiteAccountStatsView `json:"observation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Observation == nil {
		t.Fatal("invalid observation", err)
	}
	return out.Observation
}
func (v *m8StatsRuntimeFixture) saveBeforeRun(t *testing.T) {
	t.Helper()
	m8StatsConfigReceipt(t, v.request(v.ctx, "PUT", v.path()+"/config", v.body(0, true), "runtime-enable", v.cookie, ""))
	if !v.predicate(`SELECT NOT EXISTS(SELECT 1 FROM site_account_stats_attempt) AND NOT EXISTS(SELECT 1 FROM job WHERE kind='site_account_stats_collect')`) || v.statsCalls.Load() != 0 || v.probeCalls.Load() != 0 {
		t.Fatal("settings save performed collection work before Run")
	}
	view := v.view(t, v.request(v.ctx, "GET", v.path(), "", "", v.cookie, ""))
	if view.Stats != nil || view.ObservedAt != nil || view.CollectionState != "never_collected" {
		t.Fatal("configuration manufactured an observation")
	}
}
func (v *m8StatsRuntimeFixture) success(t *testing.T) *ports.SiteAccountStatsView {
	t.Helper()
	v.wait(t, "single successful native collection", func() bool {
		return v.predicate(`SELECT count(*)=1 AND bool_and(state='succeeded') FROM site_account_stats_attempt`)
	})
	view := v.view(t, v.requestHTTP(t, "GET", v.path(), "", ""))
	s := view.Stats
	if s == nil || s.AccountID != "42" || s.Uploaded == nil || s.Downloaded == nil || s.Uploaded.Value != "2.00" || s.Downloaded.Value != "1.00" || s.Uploaded.Unit != "GB" || s.Downloaded.Unit != "GB" || !s.Uploaded.Approximate || !s.Downloaded.Approximate || s.Ratio.State != "finite" || s.Ratio.Value != "2.000" || s.Bonus == nil || *s.Bonus != "1234.5" {
		t.Fatal("native displayed decimals/identity were not preserved")
	}
	if view.Stale || view.CollectionState != "succeeded" || view.SampleConfigVersion != 1 || view.SampleSourceVersion != v.source.ConfigVersion || view.Config.Version != 1 || view.ObservedAt == nil || view.LastAttemptAt == nil || view.LastAttemptAt.After(*view.ObservedAt) || view.ObservedAt.After(time.Now()) {
		t.Fatal("invalid successful timestamps/version binding")
	}
	if v.statsCalls.Load() != 1 || !v.predicate(`SELECT count(*)=1 AND bool_and(j.state='succeeded' AND j.attempts=1 AND j.max_attempts=1 AND a.sample=$3::jsonb AND a.finished_at=$1 AND a.started_at=$2) FROM site_account_stats_attempt a JOIN job j ON j.id=a.job_id`, view.ObservedAt, view.LastAttemptAt, mustM8ConfigJSON(t, view.Stats)) {
		t.Fatal("collection did not persist one fenced attempt/sample/job")
	}
	return view
}
func (v *m8StatsRuntimeFixture) protected(t *testing.T) string {
	t.Helper()
	return m8ConfigSnapshot(t, v.m7ScrapeFixture, "media", "media_identity", "media_unit", "subscription", "wanted_item", "library_file", "library_file_unit", "unit_fact", "download_task", "download_requirement", "acquisition_reservation", "unit_reservation")
}

// Snapshot every public business table. Real Cookie authentication deliberately
// touches app_session.last_seen_at; session bookkeeping is outside the GET's
// business read-only claim. No scheduler remains alive during this comparison.
func (v *m8StatsRuntimeFixture) businessSnapshot(t *testing.T) string {
	t.Helper()
	rows, err := v.pool.Query(v.ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename<>'app_session' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	return m8ConfigSnapshot(t, v.m7ScrapeFixture, tables...)
}
func (v *m8StatsRuntimeFixture) readonly(t *testing.T) {
	t.Helper()
	v.stop(t)
	before := v.businessSnapshot(t)
	calls, probes := v.statsCalls.Load(), v.probeCalls.Load()
	v.view(t, v.request(v.ctx, "GET", v.path(), "", "", v.cookie, ""))
	if before != v.businessSnapshot(t) || calls != v.statsCalls.Load() || probes != v.probeCalls.Load() || v.unexpected.Load() != 0 {
		t.Fatal("standalone authenticated GET mutated business state/budget or called source")
	}
}
func (v *m8StatsRuntimeFixture) sampleBytes(t *testing.T) string {
	t.Helper()
	var out string
	if err := v.pool.QueryRow(v.ctx, `SELECT to_jsonb(a)::text FROM site_account_stats_attempt a WHERE state='succeeded'`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestM8SiteAccountStatsRuntimePositivePG(t *testing.T) {
	v := m8NewStatsRuntimeFixture(t, false)
	protected := v.protected(t)
	v.saveBeforeRun(t)
	v.start(t)
	v.success(t)
	v.readonly(t)
	if protected != v.protected(t) {
		t.Fatal("statistics collection changed protected business graph")
	}
	t.Logf("M8_STATS_RUNTIME_POSITIVE_PASS scheduled_job=1 native_stats_GET=1 health_probe_GET=%d reserved_before_native=true precise_decimal=true readonly_GET=true protected_graph_unchanged=true", v.probeCalls.Load())
}
func TestM8SiteAccountStatsRuntimeFailurePG(t *testing.T) {
	v := m8NewStatsRuntimeFixture(t, false)
	protected := v.protected(t)
	v.saveBeforeRun(t)
	v.start(t)
	old := v.success(t)
	sample := v.sampleBytes(t)
	for i, mode := range []int32{429, 403} {
		v.wait(t, "shared source budget available", func() bool {
			return v.predicate(`SELECT next_query_at<=clock_timestamp() AND coalesce(rate_limit_until<=clock_timestamp(),true) FROM indexer WHERE id=$1`, v.source.ID)
		})
		v.mode.Store(mode)
		version := int64(i + 1)
		m8StatsConfigReceipt(t, v.requestHTTP(t, "PUT", v.path()+"/config", v.body(version, true), fmt.Sprintf("runtime-resave-%d", version)))
		v.wait(t, "failed native collection", func() bool {
			return v.predicate(`SELECT EXISTS(SELECT 1 FROM site_account_stats_attempt WHERE config_version=$1 AND state='failed')`, version+1)
		})
		view := v.view(t, v.requestHTTP(t, "GET", v.path(), "", ""))
		code := "rate_limited"
		if mode == 403 {
			code = "blocked"
		}
		if !view.Stale || view.CollectionState != "failed" || view.FailureCode != code || view.Config.Version != version+1 || view.SampleConfigVersion != 1 || view.SampleSourceVersion != v.source.ConfigVersion || mustM8ConfigJSON(t, view.Stats) != mustM8ConfigJSON(t, old.Stats) || mustM8ConfigJSON(t, view.ObservedAt) != mustM8ConfigJSON(t, old.ObservedAt) || sample != v.sampleBytes(t) {
			t.Fatal("failed refresh replaced original sample/time or lost safe failure classification")
		}
		if mode == 429 && !v.predicate(`SELECT i.rate_limit_until>=a.finished_at+interval '2.8 seconds' AND i.next_query_at>=i.rate_limit_until FROM indexer i JOIN site_account_stats_attempt a ON a.indexer_id=i.id WHERE a.config_version=2`) {
			t.Fatal("native Retry-After was not applied to shared source budget")
		}
	}
	if v.statsCalls.Load() != 3 || !v.predicate(`SELECT count(*)=3 AND count(sample)=1 AND count(*) FILTER(WHERE state='failed')=2 FROM site_account_stats_attempt`) {
		t.Fatal("unexpected refresh replay/sample count")
	}
	v.readonly(t)
	if protected != v.protected(t) {
		t.Fatal("failed statistics collection changed protected graph")
	}
	t.Log("M8_STATS_RUNTIME_FAILURE_PASS native_429_retry_after=3 shared_budget=true native_403=blocked stale=true immutable_old_sample=true immutable_observed_at=true")
}
func TestM8SiteAccountStatsRuntimeDisableLatePG(t *testing.T) {
	v := m8NewStatsRuntimeFixture(t, true)
	protected := v.protected(t)
	v.saveBeforeRun(t)
	v.start(t)
	select {
	case <-v.entered:
	case <-v.ctx.Done():
		t.Fatal("native in-flight entry deadline")
	}
	m8StatsConfigReceipt(t, v.requestHTTP(t, "PUT", v.path()+"/config", v.body(1, false), "runtime-disable"))
	if !v.predicate(`SELECT count(*)=1 AND bool_and(state='running' AND sample IS NULL) FROM site_account_stats_attempt`) {
		t.Fatal("in-flight attempt lost ownership before native return")
	}
	v.releaseOnce.Do(func() { close(v.release) })
	v.wait(t, "late callback review", func() bool {
		return v.predicate(`SELECT count(*)=1 AND bool_and(a.state='review_required' AND a.sample IS NULL AND j.state='dead') FROM site_account_stats_attempt a JOIN job j ON j.id=a.job_id`)
	})
	view := v.view(t, v.requestHTTP(t, "GET", v.path(), "", ""))
	if view.Stats != nil || view.ObservedAt != nil || view.Config.Enabled || view.Config.Version != 2 || view.CollectionState != "disabled" || !view.Stale || v.statsCalls.Load() != 1 {
		t.Fatal("disabled generation accepted late native statistics")
	}
	v.readonly(t)
	if protected != v.protected(t) {
		t.Fatal("disable late callback changed protected business graph")
	}
	t.Log("M8_STATS_RUNTIME_DISABLE_LATE_PASS real_cookie_disable=true native_response_released=true late_publish=false attempt=review_required job=dead stats=null state=disabled")
}
