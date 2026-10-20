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

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

// Only the account is SQL_PREPARED (the shared authentication fixture).
// App.Run creates roots and completes their first native scans. No media, identity, unit, subscription, Wanted, sync item or job is
// seeded. App.Run owns scheduling, native HTML/TMDB reads and business writes.
type m8WishFixture struct {
	f                        *m7ScrapeFixture
	runtime                  *m8SubtitleRuntimeFixture
	ctx                      context.Context
	base                     string
	movie, tv                uuid.UUID
	wishCalls, metadataCalls atomic.Int32
	seasonReturned           atomic.Bool
	entered, release         chan struct{}
	releaseOnce              sync.Once
}

type m8WishRoute struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r m8WishRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || (req.URL.Host != "movie.douban.com" && req.URL.Host != "api.themoviedb.org") || req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
		return nil, errors.New("unexpected wish acceptance destination")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.target.Scheme, r.target.Host
	clone.URL, clone.Host = &u, r.target.Host
	return r.transport.RoundTrip(clone)
}

func m8WishHTML(kind, scenario string) string {
	heading, label, title, id := "电影", "电影", "Owned Wish Movie", "1234567"
	if kind == "tv" {
		heading, label, title, id = "电视剧", "电视", m6JourneyTitle, "2345678"
	}
	total := 1
	if scenario == "gap" {
		total = 2
	}
	year := "2020-01-01(美国) / 演员 / 90分钟"
	if scenario == "unknown" {
		year = "未知 / 演员 / 90分钟"
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"></head><body><div id="db-usr-profile"><div class="pic"><a href="/people/exherb/"><img src="https://img9.doubanio.com/icon/u3121296-54.jpg"></a></div><h1>exHerb想看的%s(%d)</h1></div><div class="tabs-more"><span class="gray">影片类型:</span><a class="lnk-tab-more"><span>%s</span></a><ul><li>全部</li></ul></div><span class="subject-num">1-%d&nbsp;/&nbsp;%d</span><div class="grid-view"><div class="item comment-item"><div class="pic"><a href="https://movie.douban.com/subject/%s/"><img></a></div><div class="info"><ul><li class="title"><a href="https://movie.douban.com/subject/%s/"><em>%s</em></a></li><li class="intro">%s</li></ul></div></div></div></body></html>`, heading, total, label, total, total, id, id, title, year)
}

func m8NewWishFixture(t *testing.T, scenario string) *m8WishFixture {
	t.Helper()
	// Never consume real metadata credentials from the developer's environment.
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	f := m7NewScrapeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	v := &m8WishFixture{f: f, ctx: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/people/") {
			v.wishCalls.Add(1)
			q := r.URL.Query()
			if r.Method != "GET" || r.URL.Path != "/people/exherb/wish" || q.Get("start") != "0" || q.Get("sort") != "time" || q.Get("filter") != "all" || q.Get("mode") != "grid" || (q.Get("type") != "movie" && q.Get("type") != "tv") || len(q) != 5 {
				t.Error("native wish request contract mismatch")
				w.WriteHeader(400)
				return
			}
			if scenario == "denied" {
				w.WriteHeader(403)
				return
			}
			if scenario == "timeout" {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, m8WishHTML(q.Get("type"), scenario))
			return
		}
		v.metadataCalls.Add(1)
		if r.Method != "GET" || r.URL.Query().Get("api_key") != "owned-wish-tmdb" || r.URL.Query().Get("language") != "zh-CN" {
			t.Error("native TMDB credential/locale mismatch")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		movieJSON := `{"id":551,"media_type":"movie","title":"Owned Wish Movie","original_title":"Owned Wish Movie","release_date":"2020-01-01","overview":"Owned fixture","vote_average":8}`
		tvJSON := `{"id":550,"media_type":"tv","name":"Native Automatic Series","original_name":"Native Automatic Series","first_air_date":"2020-01-01","overview":"Owned fixture","vote_average":8,"seasons":[{"season_number":1,"episode_count":3},{"season_number":2,"episode_count":1}]}`
		var body string
		switch r.URL.Path {
		case "/3/search/movie":
			body = `{"page":1,"total_pages":1,"total_results":1,"results":[` + movieJSON + `]}`
		case "/3/search/tv":
			body = `{"page":1,"total_pages":1,"total_results":1,"results":[` + tvJSON + `]}`
		case "/3/movie/551":
			body = movieJSON
		case "/3/tv/550":
			body = tvJSON
		case "/3/movie/551/credits", "/3/tv/550/credits":
			body = `{"cast":[]}`
		case "/3/tv/550/season/1":
			if scenario == "disable" {
				close(v.entered)
				select {
				case <-v.release:
				case <-r.Context().Done():
					return
				}
			}
			body = `{"id":5501,"season_number":1,"episodes":[{"id":550101,"name":"Future","season_number":1,"episode_number":1,"air_date":"2099-01-01"},{"id":550103,"name":"Sparse aired","season_number":1,"episode_number":3,"air_date":"2020-01-15"},{"id":550107,"name":"Unknown","season_number":1,"episode_number":7,"air_date":null}]}`
		case "/3/tv/550/season/2":
			body = `{"id":5502,"season_number":2,"episodes":[{"id":550209,"season_number":2,"episode_number":9,"air_date":null}]}`
		default:
			t.Error("unexpected native metadata request")
			w.WriteHeader(503)
			return
		}
		_, writeErr := io.WriteString(w, body)
		if scenario == "disable" && r.URL.Path == "/3/tv/550/season/1" && writeErr == nil && r.Context().Err() == nil {
			v.seasonReturned.Store(true)
		}
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal("owned fixture URL")
	}
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	cfgCopy := *f.app.Config()
	cfg := &cfgCopy
	cfg.DataDir = t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("owned App listener")
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal("release owned listener")
	}
	v.base = "http://" + cfg.Server.HTTPAddr
	nativeTimeout := 2 * time.Second
	if scenario == "disable" {
		nativeTimeout = 15 * time.Second
	}
	v.runtime = &m8SubtitleRuntimeFixture{pool: f.pool, cfg: cfg, client: &http.Client{Transport: m8WishRoute{target, tr}, Timeout: nativeTimeout}}
	// Release a blocked fixture before joining App; join before HTTP and DB cleanup.
	t.Cleanup(func() { v.releaseOnce.Do(func() { close(v.release) }); v.runtime.stop(t) })
	v.runtime.start(t)
	v.wait(t, "HTTP readiness", func() bool {
		req, _ := http.NewRequestWithContext(ctx, "GET", v.base+"/api/v1/auth/status", nil)
		res, e := (&http.Client{Timeout: time.Second}).Do(req)
		if e != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == 200
	})
	v.wait(t, "native root baselines", func() bool {
		var movie, tv uuid.UUID
		err := f.pool.QueryRow(ctx, `SELECT (SELECT id FROM library_root WHERE media_type='movie' AND health='online' AND baseline_completed),(SELECT id FROM library_root WHERE media_type='tv' AND health='online' AND baseline_completed)`).Scan(&movie, &tv)
		if err != nil || movie == uuid.Nil || tv == uuid.Nil {
			return false
		}
		v.movie, v.tv = movie, tv
		return true
	})
	v.check(t, "native root scan identity", `SELECT count(DISTINCT r.id)=2 FROM library_root r JOIN scan_run sr ON sr.root_id=r.id AND sr.generation=r.generation JOIN scan_finalization sf ON sf.scan_run_id=sr.id WHERE r.id=ANY($1::uuid[]) AND sr.state='succeeded' AND sf.completed_at IS NOT NULL`, []uuid.UUID{v.movie, v.tv})
	v.request(t, "PUT", "/api/v1/settings/tmdb.api_key", `{"expected_version":0,"value":{"api_key":"owned-wish-tmdb"}}`, "owned-metadata-setting", 200)
	return v
}

func (v *m8WishFixture) wait(t *testing.T, phase string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-v.ctx.Done():
			t.Fatalf("wish acceptance deadline phase=%s", phase)
		case <-ticker.C:
		}
	}
}
func (v *m8WishFixture) request(t *testing.T, method, path, body, key string, status int) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequestWithContext(v.ctx, method, v.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal("owned request construction")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", v.base)
	req.AddCookie(v.f.cookie)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed phase=%s", method)
	}
	defer func() { _ = res.Body.Close() }()
	w := httptest.NewRecorder()
	w.Code = res.StatusCode
	for name, values := range res.Header {
		w.Header()[name] = append([]string(nil), values...)
	}
	if _, err = io.Copy(w.Body, io.LimitReader(res.Body, 1<<20)); err != nil {
		t.Fatal("HTTP response read")
	}
	if res.StatusCode != status {
		t.Fatalf("HTTP phase=%s status=%d want=%d", path, res.StatusCode, status)
	}
	return w
}
func (v *m8WishFixture) input(version int, enabled, tv bool) string {
	extra := ""
	if tv {
		extra = fmt.Sprintf(`,"tv_root_id":%q`, v.tv)
	}
	return fmt.Sprintf(`{"douban_uid":"exherb","enabled":%t,"movie_root_id":%q,"expected_version":%d%s}`, enabled, v.movie, version, extra)
}
func (v *m8WishFixture) check(t *testing.T, phase, query string, args ...any) {
	t.Helper()
	var ok bool
	if err := v.f.pool.QueryRow(v.ctx, query, args...).Scan(&ok); err != nil || !ok {
		t.Fatalf("SQL acceptance failed phase=%s", phase)
	}
}
func (v *m8WishFixture) terminal(t *testing.T, status string) {
	t.Helper()
	var actual string
	v.wait(t, "terminal "+status, func() bool {
		err := v.f.pool.QueryRow(v.ctx, `SELECT status FROM douban_wish_sync_run`).Scan(&actual)
		return err == nil && (actual == "succeeded" || actual == "partial" || actual == "review_required")
	})
	if actual != status {
		var items, created, matched, unavailable int
		_ = v.f.pool.QueryRow(v.ctx, `SELECT count(*),count(*) FILTER(WHERE state='created'),count(*) FILTER(WHERE state='matched'),count(*) FILTER(WHERE state='unavailable') FROM douban_wish_sync_item`).Scan(&items, &created, &matched, &unavailable)
		t.Fatalf("terminal run=%s want=%s items=%d created=%d matched=%d unavailable=%d HTML_GET=%d TMDB_GET=%d", actual, status, items, created, matched, unavailable, v.wishCalls.Load(), v.metadataCalls.Load())
	}
}
func (v *m8WishFixture) noCreation(t *testing.T) {
	v.check(t, "closed business graph", `SELECT NOT EXISTS(SELECT 1 FROM media) AND NOT EXISTS(SELECT 1 FROM media_identity) AND NOT EXISTS(SELECT 1 FROM media_unit) AND NOT EXISTS(SELECT 1 FROM subscription) AND NOT EXISTS(SELECT 1 FROM wanted_item) AND NOT EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'subscription.creation:%') AND NOT EXISTS(SELECT 1 FROM outbox_event WHERE aggregate_type LIKE 'subscription%')`)
}
func (v *m8WishFixture) due(t *testing.T) {
	t.Helper()
	if err := postgres.NewDoubanWishSyncStore(v.f.pool, v.runtime.app.IDGen()).EnqueueDue(v.ctx); err != nil {
		t.Fatal("due scheduling probe failed")
	}
}

func TestM8DoubanWishPositivePG(t *testing.T) {
	v := m8NewWishFixture(t, "positive")
	qbState := v.prepareAutomaticTV(t)
	body := v.boundInput(qbState)
	first := m8ConfigReceipt(t, v.request(t, "PUT", m8ConfigPath, body, "wish-original", 200))
	v.terminal(t, "succeeded")
	v.check(t, "two items and page jobs", `SELECT (SELECT count(*) FROM douban_wish_sync_item WHERE state='created')=2 AND (SELECT count(*) FROM job WHERE kind='douban_wish_sync')=2 AND (SELECT count(*) FROM job WHERE kind='douban_wish_sync' AND state='succeeded' AND attempts=1)=2 AND (SELECT count(*) FROM subscription)=2 AND (SELECT count(*) FROM media)=2`)
	v.check(t, "owner root identity and original keys", `SELECT count(*)=2 AND bool_and(s.user_id=$1 AND s.root_id=CASE i.media_type WHEN 'movie' THEN $2::uuid ELSE $3::uuid END AND m.media_type=i.media_type AND i.receipt IS NOT NULL AND EXISTS(SELECT 1 FROM idempotency_record k WHERE k.actor_id=$1::uuid::text AND k.idempotency_key='subscription.creation:'||i.command_key) AND EXISTS(SELECT 1 FROM media_identity mi WHERE mi.media_id=m.id AND mi.namespace='tmdb.'||i.media_type AND mi.external_id=CASE i.media_type WHEN 'movie' THEN '551' ELSE '550' END)) FROM douban_wish_sync_item i JOIN subscription s ON s.id=i.subscription_id JOIN media m ON m.id=s.media_id`, v.f.admin, v.movie, v.tv)
	v.check(t, "real sparse Wanted units", `SELECT count(*)=4 AND array_agg(u.season_number::text||':'||u.episode_number::text ORDER BY u.season_number,u.episode_number)=ARRAY['1:1','1:3','1:7','2:9'] AND bool_and(w.subscription_id=s.id AND u.media_id=s.media_id) FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id JOIN media m ON m.id=s.media_id JOIN media_unit u ON u.id=w.unit_id WHERE m.media_type='tv'`)
	var pages []byte
	if err := v.f.pool.QueryRow(v.ctx, `SELECT page FROM douban_wish_sync_page WHERE media_type='tv'`).Scan(&pages); err != nil {
		t.Fatal("page evidence unavailable")
	}
	var page ports.DoubanWishPage
	if json.Unmarshal(pages, &page) != nil || !page.WindowComplete || len(page.Items) != 1 {
		t.Fatal("native page evidence mismatch")
	}
	v.automaticTVIngestion(t, qbState)
	before := v.graph(t)
	calls, metadataCalls := v.wishCalls.Load(), v.metadataCalls.Load()
	replay := m8ConfigReceipt(t, v.request(t, "PUT", m8ConfigPath, body, "wish-original", 200))
	receipt := m8ConfigReceipt(t, v.request(t, "GET", m8ConfigPath+"/commands/wish-original", "", "", 200))
	if mustM8ConfigJSON(t, first) != mustM8ConfigJSON(t, replay) || mustM8ConfigJSON(t, first) != mustM8ConfigJSON(t, receipt) {
		t.Fatal("original receipt changed")
	}
	current := v.request(t, "GET", m8ConfigPath, "", "", 200)
	var state struct {
		Config *ports.DoubanSyncConfig `json:"config"`
	}
	if json.Unmarshal(current.Body.Bytes(), &state) != nil || state.Config == nil || state.Config.ID != first.Config.ID || state.Config.Version != 1 {
		t.Fatal("configuration GET changed meaning")
	}
	v.request(t, "POST", "/api/v1/sync/douban", `{"douban_uid":"exherb"}`, "legacy-wish", 503)
	v.runtime.stop(t)
	v.due(t)
	v.runtime.start(t)
	v.wait(t, "restart readiness", func() bool { return v.httpReady() })
	v.due(t)
	if calls != v.wishCalls.Load() || metadataCalls != v.metadataCalls.Load() || before != v.graph(t) {
		t.Fatal("replay/restart changed import graph")
	}
	v.check(t, "one period only", `SELECT (SELECT count(*) FROM douban_wish_sync_run)=1 AND (SELECT count(*) FROM job WHERE kind='douban_wish_sync')=2`)
	v.check(t, "Wanted not duplicated", `SELECT count(*)=5 AND bool_and(u.media_id=s.media_id AND s.user_id=$1) FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id JOIN media_unit u ON u.id=w.unit_id`, v.f.admin)
	t.Logf("M8_DOUBAN_WISH_POSITIVE_PASS runs=1 items_created=2 page_jobs=2 inventory=SQL_PREPARED_ACCOUNT_ONLY roots=NATIVE_SCAN native_HTML_GET=%d native_TMDB_GET=%d Add=1 hardlink=true ingestion=IMPORTED", v.wishCalls.Load(), v.metadataCalls.Load())
}

func TestM8DoubanWishFailurePG(t *testing.T) {
	for _, scenario := range []string{"gap", "unknown", "denied", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			v := m8NewWishFixture(t, scenario)
			v.request(t, "PUT", m8ConfigPath, v.input(0, true, false), "wish-original", 200)
			status := "partial"
			if scenario == "denied" || scenario == "timeout" {
				status = "review_required"
			}
			v.terminal(t, status)
			switch scenario {
			case "gap":
				v.check(t, "visible only", `SELECT (SELECT count(*) FROM douban_wish_sync_item WHERE state='created')=1 AND (SELECT count(*) FROM subscription)=1 AND (SELECT count(*) FROM media)=1 AND (SELECT incomplete FROM douban_wish_sync_run)`)
				var raw []byte
				var p ports.DoubanWishPage
				if v.f.pool.QueryRow(v.ctx, `SELECT page FROM douban_wish_sync_page`).Scan(&raw) != nil || json.Unmarshal(raw, &p) != nil || p.WindowComplete || len(p.Items) != 1 || p.WindowEnd != 2 {
					t.Fatal("gap lost native window evidence")
				}
			case "unknown":
				v.noCreation(t)
				v.check(t, "unknown item durable", `SELECT count(*)=1 AND bool_and(state='unresolved' AND subscription_id IS NULL AND receipt IS NULL) FROM douban_wish_sync_item`)
				if v.metadataCalls.Load() != 0 {
					t.Fatal("unknown year searched metadata")
				}
			default:
				v.noCreation(t)
				v.check(t, "access failure no empty page", `SELECT NOT EXISTS(SELECT 1 FROM douban_wish_sync_item) AND NOT EXISTS(SELECT 1 FROM douban_wish_sync_page WHERE page IS NOT NULL)`)
			}
			v.runtime.stop(t)
			calls := v.wishCalls.Load()
			v.due(t)
			v.runtime.start(t)
			v.wait(t, "failure restart readiness", func() bool { return v.httpReady() })
			v.due(t)
			v.check(t, "no automatic retry", `SELECT (SELECT count(*) FROM douban_wish_sync_run)=1 AND (SELECT count(*) FROM job WHERE kind='douban_wish_sync')=1`)
			if v.wishCalls.Load() != calls || calls != 1 {
				t.Fatal("failure retried native GET")
			}
			t.Logf("M8_DOUBAN_WISH_FAILURE_PASS scenario=%s run=%s native_GET=1", scenario, status)
		})
	}
}

func TestM8DoubanWishDisableLatePG(t *testing.T) {
	v := m8NewWishFixture(t, "disable")
	v.request(t, "PUT", m8ConfigPath, fmt.Sprintf(`{"douban_uid":"exherb","enabled":true,"tv_root_id":%q,"expected_version":0}`, v.tv), "wish-original", 200)
	select {
	case <-v.entered:
	case <-v.ctx.Done():
		t.Fatal("creation metadata barrier not reached")
	}
	v.check(t, "network outside transaction before creation", `SELECT count(*)=1 AND bool_and(state='matched' AND subscription_id IS NULL) FROM douban_wish_sync_item`)
	v.noCreation(t)
	v.request(t, "PUT", m8ConfigPath, fmt.Sprintf(`{"douban_uid":"exherb","enabled":false,"tv_root_id":%q,"expected_version":1}`, v.tv), "wish-disable", 200)
	v.releaseOnce.Do(func() { close(v.release) })
	v.terminal(t, "review_required")
	if !v.seasonReturned.Load() {
		t.Fatal("blocked native metadata did not return successfully")
	}
	v.noCreation(t)
	v.check(t, "authoritative disabled version", `SELECT count(*)=1 AND bool_and(version=2 AND NOT enabled) FROM douban_sync_config`)
	v.check(t, "no sync success", `SELECT NOT EXISTS(SELECT 1 FROM job WHERE kind='douban_wish_sync' AND state='succeeded') AND NOT EXISTS(SELECT 1 FROM douban_wish_sync_item WHERE state='created' OR receipt IS NOT NULL)`)
	t.Log("M8_DOUBAN_WISH_DISABLE_LATE_PASS metadata_returned=true authoritative_config_version=2 subscriptions=0 wanted=0 creation_receipts=0")
}

// All endpoints are explicitly configured before the wish configuration is saved.
func (v *m8WishFixture) prepareAutomaticTV(t *testing.T) *m6Journey {
	t.Helper()
	qbState := &m6Journey{application: v.runtime.app, root: v.tv}
	if err := v.f.pool.QueryRow(v.ctx, `SELECT path FROM library_root WHERE id=$1`, v.tv).Scan(&qbState.rootPath); err != nil {
		t.Fatal("native TV root unavailable")
	}
	qbState.raw, qbState.hash, qbState.names, qbState.payloads = m6JourneyTorrent()
	for _, payload := range qbState.payloads {
		qbState.total += int64(len(payload))
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { qbState.serveSource(t, w, r) }))
	t.Cleanup(source.Close)
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { qbState.serveQB(t, w, r) }))
	t.Cleanup(qb.Close)
	t.Cleanup(func() { v.runtime.stop(t) })
	parseID := func(w *httptest.ResponseRecorder) uuid.UUID {
		var x struct {
			ID uuid.UUID `json:"id"`
		}
		if json.Unmarshal(w.Body.Bytes(), &x) != nil || x.ID == uuid.Nil {
			t.Fatal("endpoint receipt missing ID")
		}
		return x.ID
	}
	idx := parseID(v.request(t, "POST", "/api/v1/indexers", fmt.Sprintf(`{"name":"Owned wish source","protocol":"torznab","url":%q,"api_key":"owned-tv-source-key","categories":[5000]}`, source.URL), "wish-source", 201))
	v.request(t, "POST", "/api/v1/indexers/"+idx.String()+"/test", `{}`, "wish-source-probe", 200)
	downloader := parseID(v.request(t, "POST", "/api/v1/download-clients", fmt.Sprintf(`{"name":"Owned wish qB","client_type":"qbittorrent","url":%q,"username":"owned-tv","password":"owned-tv-client-key","remote_path_map":{}}`, qb.URL), "wish-client", 201))
	qbState.client, qbState.indexer = downloader, idx
	return qbState
}

func (v *m8WishFixture) boundInput(qbState *m6Journey) string {
	return strings.TrimSuffix(v.input(0, true, true), "}") + fmt.Sprintf(`,"bindings":{"indexer_ids":[%q],"downloader_id":%q}}`, qbState.indexer, qbState.client)
}

func (v *m8WishFixture) automaticTVIngestion(t *testing.T, qbState *m6Journey) {
	t.Helper()
	var sub uuid.UUID
	if v.f.pool.QueryRow(v.ctx, `SELECT id FROM subscription WHERE root_id=$1 AND user_id=$2`, v.tv, v.f.admin).Scan(&sub) != nil {
		t.Fatal("imported TV subscription unavailable")
	}
	v.check(t, "config-bound automatic intent", `SELECT count(*)=2 AND bool_and(downloader_id=$1 AND indexer_ids=to_jsonb(ARRAY[$2::uuid])) FROM subscription`, qbState.client, qbState.indexer)
	v.wait(t, "automatic TV native query", func() bool {
		var ok bool
		return v.f.pool.QueryRow(v.ctx, `SELECT EXISTS(SELECT 1 FROM automatic_tv_query q JOIN indexer_query i ON i.id=q.query_id JOIN automatic_tv_round r ON r.id=q.round_id JOIN wanted_item w ON w.id=r.wanted_id JOIN media_unit u ON u.id=w.unit_id JOIN automatic_tv_source src ON src.round_id=q.round_id AND src.indexer_id=q.indexer_id JOIN job j ON j.id=src.job_id WHERE r.subscription_id=$1 AND r.owner_id=$2 AND q.indexer_id=$3 AND i.query_type='tv_episode' AND i.state='succeeded' AND j.attempts=1 AND u.season_number=1 AND u.episode_number=3)`, sub, v.f.admin, qbState.indexer).Scan(&ok) == nil && ok
	})
	v.wait(t, "automatic TV physical import", func() bool {
		var imported bool
		return v.f.pool.QueryRow(v.ctx, `SELECT EXISTS(SELECT 1 FROM download_task d JOIN download_requirement r ON r.operation_id=d.operation_id JOIN wanted_item w ON w.unit_id=r.unit_id JOIN subscription s ON s.id=w.subscription_id AND s.root_id=r.root_id JOIN unit_fact f ON f.unit_id=r.unit_id AND f.root_id=r.root_id AND f.stage='imported' AND f.revoked_at IS NULL WHERE s.id=$1 AND r.state='imported' AND d.state='completed' AND d.organization_state='completed')`, sub).Scan(&imported) == nil && imported
	})
	qbState.assertImported(t)
	v.check(t, "single automatic TV operation", `SELECT (SELECT count(*) FROM download_task)=1 AND (SELECT count(*) FROM outbox_event WHERE event_type='media.imported')=1 AND (SELECT count(*) FROM douban_wish_sync_run)=1 AND (SELECT count(*) FROM douban_wish_sync_item WHERE state='created')=2`)
	before, calls := v.graph(t), v.wishCalls.Load()
	v.request(t, "PUT", m8ConfigPath, v.boundInput(qbState), "wish-original", 200)
	v.request(t, "GET", m8ConfigPath+"/commands/wish-original", "", "", 200)
	if before != v.graph(t) || calls != v.wishCalls.Load() || qbState.adds.Load() != 1 {
		t.Fatal("post-ingestion original-key replay changed business graph")
	}
	t.Log("M8_DOUBAN_WISH_TV_INGESTION_PASS origin=IMPORTED_WANTED authorization=FROZEN_CONFIG_BINDINGS scheduler=NATIVE TV_query=1 torrent_fetch=1 Add=1 hardlink=true ingestion=IMPORTED")
}

func (v *m8WishFixture) httpReady() bool {
	req, err := http.NewRequestWithContext(v.ctx, "GET", v.base+"/api/v1/auth/status", nil)
	if err != nil {
		return false
	}
	res, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// Compare immutable import identities/results; evaluation scheduling clocks are
// deliberately excluded and Wanted membership is checked separately.
func (v *m8WishFixture) graph(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"douban_wish_sync_run", "douban_wish_sync_item", "media", "media_identity", "media_unit", "subscription"} {
		var value string
		if err := v.f.pool.QueryRow(v.ctx, `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+table+` t) s`).Scan(&value); err != nil {
			t.Fatalf("import graph unavailable phase=%s", table)
		}
		out.WriteString(table)
		out.WriteString(value)
	}
	return out.String()
}
