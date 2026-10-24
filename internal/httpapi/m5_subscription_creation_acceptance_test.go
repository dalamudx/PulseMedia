package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
)

const m5CreationPath = "/api/v1/subscriptions"
const m5CreationPassword = "owned-creation-login-2026"
const m5CreationAPIKey = "owned-creation-tmdb-key"

// Only native TMDB outbound HTTP is routed. The real dynamic provider, encrypted
// settings, Router, Cookie authentication, command service and PG UoW all run.
type m5CreationRoute struct {
	target    *url.URL
	transport http.RoundTripper
	protocol  *m5CreationProtocol
}

func (r m5CreationRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.protocol != nil {
		r.protocol.mu.Lock()
		transport := r.protocol.tvdbTransport
		r.protocol.mu.Unlock()
		if transport != nil {
			return transport.RoundTrip(req)
		}
	}
	if r.protocol != nil && (req.URL.Host == "webservice.fanart.tv" || req.URL.Host == "assets.fanart.tv" || req.URL.Host == "api.themoviedb.org" && strings.HasSuffix(req.URL.Path, "/external_ids")) {
		r.protocol.mu.Lock()
		transport := r.protocol.artworkTransport
		r.protocol.mu.Unlock()
		if transport != nil {
			return transport.RoundTrip(req)
		}
	}
	allowed := (req.URL.Host == "api.themoviedb.org" && strings.HasPrefix(req.URL.Path, "/3/")) || (req.URL.Host == "api.bgm.tv" && req.URL.Path == "/calendar")
	if req.URL.Scheme != "https" || !allowed {
		return nil, errors.New("unexpected outbound metadata destination")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.target.Scheme, r.target.Host
	clone.URL, clone.Host = &u, r.target.Host
	return r.transport.RoundTrip(clone)
}

type m5CreationProtocol struct {
	mu               sync.Mutex
	artworkTransport http.RoundTripper
	tvdbTransport    http.RoundTripper
	bodies           map[string]string
	calls            map[string]int
	calendarStatus   int
	blockedPath      string
	entered, release chan struct{}
}

func (p *m5CreationProtocol) set(path, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies[path] = body
}
func (p *m5CreationProtocol) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		n += c
	}
	return n
}
func (p *m5CreationProtocol) pathCount(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[path]
}
func (p *m5CreationProtocol) block(path string) (<-chan struct{}, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blockedPath, p.entered, p.release = path, make(chan struct{}), make(chan struct{})
	release := p.release
	var once sync.Once
	return p.entered, func() { once.Do(func() { close(release) }) }
}
func (p *m5CreationProtocol) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if p.serveBangumi(t, w, r) {
		return
	}
	if r.Method != "GET" || r.URL.Query().Get("api_key") != m5CreationAPIKey || r.URL.Query().Get("language") != "zh-CN" {
		t.Error("native TMDB lost encrypted setting credential or locale")
		w.WriteHeader(400)
		return
	}
	p.mu.Lock()
	p.calls[r.URL.Path]++
	body, ok := p.bodies[r.URL.Path]
	var release <-chan struct{}
	if p.blockedPath == r.URL.Path {
		release = p.release
		select {
		case <-p.entered:
		default:
			close(p.entered)
		}
	}
	p.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}
	if !ok {
		t.Errorf("unexpected native TMDB path %s", r.URL.Path)
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func m5CreationJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func m5CreationTV(p *m5CreationProtocol, id int, expanded, specials, empty bool) {
	seasons := []map[string]any{}
	if specials {
		seasons = append(seasons, map[string]any{"season_number": 0, "episode_count": 1})
	}
	n1, n3 := 2, 0
	if expanded {
		n1, n3 = 3, 1
	}
	if empty {
		n1, n3 = 0, 0
	}
	seasons = append(seasons, map[string]any{"season_number": 1, "episode_count": n1}, map[string]any{"season_number": 2, "episode_count": 0}, map[string]any{"season_number": 3, "episode_count": n3})
	if !empty {
		seasons[len(seasons)-2]["episode_count"] = 2
	}
	p.set(fmt.Sprintf("/3/tv/%d", id), m5CreationJSON(map[string]any{"id": id, "name": "Native sparse television", "original_name": "Native sparse television", "first_air_date": "2020-01-01", "seasons": seasons}))
	episode := func(sn, en int, date any) map[string]any {
		return map[string]any{"id": id*10000 + sn*100 + en, "season_number": sn, "episode_number": en, "air_date": date}
	}
	s1 := []map[string]any{episode(1, 1, "2020-01-01"), episode(1, 3, "2020-01-15")}
	if expanded {
		s1 = append(s1, episode(1, 5, "2020-01-29"))
	}
	p.set(fmt.Sprintf("/3/tv/%d/season/1", id), m5CreationJSON(map[string]any{"season_number": 1, "episodes": s1}))
	p.set(fmt.Sprintf("/3/tv/%d/season/2", id), m5CreationJSON(map[string]any{"season_number": 2, "episodes": []map[string]any{episode(2, 1, "2099-01-01"), episode(2, 3, nil)}}))
	p.set(fmt.Sprintf("/3/tv/%d/season/3", id), m5CreationJSON(map[string]any{"season_number": 3, "episodes": []map[string]any{episode(3, 7, nil)}}))
	p.set(fmt.Sprintf("/3/tv/%d/season/0", id), m5CreationJSON(map[string]any{"season_number": 0, "episodes": []map[string]any{episode(0, 2, "2020-01-01")}}))
	p.set(fmt.Sprintf("/3/tv/%d/credits", id), m5CreationJSON(map[string]any{"id": id, "cast": []map[string]any{
		{"id": id*100 + 1, "name": "Native TV actor", "character": "Leading role", "profile_path": "/native-tv-actor.jpg"},
		{"id": id*100 + 2, "name": "Unknown photo actor", "character": "Supporting role", "profile_path": nil},
	}}))
}
func newM5CreationAcceptance(t *testing.T, configuration ...func(*config.Config)) (*m5IndexerAcceptance, *m5CreationProtocol) {
	t.Helper()
	// Opt-in precedes all cluster/database creation. No host credentials are read.
	if os.Getenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE") != "1" {
		t.Skip("set PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE=1 with an owned PG18.6 fixture")
	}
	if !strings.HasPrefix(os.Getenv("TMPDIR"), "/data/temp/") || os.Getenv("TMP") != os.Getenv("TMPDIR") || os.Getenv("TEMP") != os.Getenv("TMPDIR") {
		t.Fatal("owned TMPDIR/TMP/TEMP under /data/temp required")
	}
	t.Setenv("TMDB_API_KEY", "")
	t.Setenv("PULSE_TMDB_API_KEY", "")
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(t.Context(), dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	p := &m5CreationProtocol{bodies: map[string]string{}, calls: map[string]int{}}
	p.set("/3/movie/550", `{"id":550,"title":"Native trusted movie","original_title":"Native trusted movie","release_date":"1999-10-15","overview":"Complete owned native metadata"}`)
	p.set("/3/movie/550/credits", `{"id":550,"cast":[{"id":55001,"name":"Native movie actor","character":"Leading role","profile_path":"/native-movie-actor.jpg"}]}`)
	m5CreationTV(p, 550, false, true, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.serve(t, w, r) }))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	cfg := config.NewDefault()
	cfg.Database.URL = dsn
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPAddr = listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	for _, configure := range configuration {
		configure(cfg)
	}
	a, err := app.New(cfg, app.WithMetadataHTTPClient(&http.Client{Transport: m5CreationRoute{target, tr, p}, Timeout: 10 * time.Second}), app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	f := &m5IndexerAcceptance{app: a, base: "http://" + cfg.Server.HTTPAddr, dataDir: cfg.DataDir}
	f.newRuntime = func() (*app.App, error) {
		for _, configure := range configuration {
			configure(cfg)
		}
		return app.New(cfg, app.WithMetadataHTTPClient(&http.Client{Transport: m5CreationRoute{target, tr, p}, Timeout: 10 * time.Second}), app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var stopRuntimeOnce sync.Once
	f.stopRuntime = func() {
		stopRuntimeOnce.Do(func() {
			cancel()
			select {
			case e := <-done:
				if e != nil {
					t.Error(e)
				}
			case <-time.After(50 * time.Second):
				t.Error("App cancellation/join exceeded shutdown budget")
			}
			if e := a.Close(); e != nil {
				t.Error(e)
			}
		})
	}
	t.Cleanup(f.stopRuntime)
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, e := (&http.Client{Timeout: time.Second}).Get(f.base + "/api/v1/auth/status")
		if e == nil {
			_ = r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("App readiness deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
	var version string
	if err = a.DBPool().QueryRow(t.Context(), "SHOW server_version").Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatal("PG18.6 required", err)
	}
	setup := f.request(t, "POST", "/api/v1/auth/setup", "", map[string]string{"username": "creation-admin", "password": m5CreationPassword}, "")
	m5IndexerStatus(t, setup, 200)
	f.session = m5CreationCookie(t, setup)
	setting := f.request(t, "PUT", "/api/v1/settings/tmdb.api_key", "creation-settings", map[string]any{"expected_version": 0, "value": map[string]any{"api_key": m5CreationAPIKey}}, f.session)
	m5IndexerStatus(t, setting, 200)
	var encoded string
	if err = a.DBPool().QueryRow(t.Context(), "SELECT value::text FROM setting WHERE key='tmdb.api_key'").Scan(&encoded); err != nil || !strings.Contains(encoded, "ciphertext") || strings.Contains(encoded, m5CreationAPIKey) {
		t.Fatal("actual TMDB setting encryption failed", err)
	}
	for _, kind := range []string{"movie", "tv"} {
		deadline = time.Now().Add(20 * time.Second)
		for {
			var path string
			e := a.DBPool().QueryRow(t.Context(), `SELECT path FROM library_root WHERE media_type=$1 AND health='online' AND baseline_completed`, kind).Scan(&path)
			if e == nil {
				if _, e = os.Stat(filepath.Join(path, ".pulsemedia_root")); e != nil {
					t.Fatal("native root identity marker", e)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("native root baseline deadline", kind, e)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	return f, p
}
func m5CreationCookie(t *testing.T, r m5IndexerHTTPResult) string {
	t.Helper()
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == "pulsemedia_session" {
			return c.Value
		}
	}
	t.Fatal("actual login/setup must issue Cookie")
	return ""
}
func m5CreationLogin(t *testing.T, f *m5IndexerAcceptance, name string) string {
	t.Helper()
	r := f.request(t, "POST", "/api/v1/auth/login", "", map[string]string{"username": name, "password": m5CreationPassword}, "")
	m5IndexerStatus(t, r, 200)
	return m5CreationCookie(t, r)
}
func m5CreationPost(t *testing.T, f *m5IndexerAcceptance, key string, body any) m5IndexerHTTPResult {
	t.Helper()
	r := f.request(t, "POST", m5CreationPath, key, body, f.session)
	m5IndexerStatus(t, r, 201)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("creation receipt must be private/no-store")
	}
	return r
}
func m5CreationID(t *testing.T, r m5IndexerHTTPResult, field string) uuid.UUID {
	t.Helper()
	v := m5IndexerObject(t, r)
	s, ok := v[field].(string)
	if !ok {
		t.Fatal("missing receipt UUID", field)
	}
	id, e := uuid.Parse(s)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func m5CreationVersion(t *testing.T, f *m5IndexerAcceptance, sub uuid.UUID) int {
	t.Helper()
	return m5InteractiveInt(t, f, `SELECT intent_version FROM subscription WHERE id=$1`, sub)
}
func m5CreationSnapshot(t *testing.T, f *m5IndexerAcceptance) string {
	t.Helper()
	// Include all creation facts/ledgers. Worker delivery/session touches and derived
	// Wanted evaluation fields are outside the intent; user facts remain included.
	const query = `SELECT jsonb_build_object(
 'media',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM media x),
 'identities',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY media_id,namespace),'[]') FROM media_identity x),
 'units',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM media_unit x),
 'episodes',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY unit_id),'[]') FROM media_unit_identity x),
 'season_proofs',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY media_id,media_version,season_number),'[]') FROM tv_season_metadata_snapshot x),
 'subscriptions',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM subscription x),
 'wanted',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,subscription_id,unit_id,intent_version,is_skipped,user_skipped,scope_excluded,is_satisfied) ORDER BY id),'[]') FROM wanted_item),
 'commands',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM idempotency_record x WHERE idempotency_key LIKE 'subscription.creation:%'),
 'audit',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM audit_log x WHERE action='subscription.creation'),
 'events',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,event_type,payload) ORDER BY id),'[]') FROM outbox_event WHERE event_type IN ('subscription.created','subscription.creation_ensured','subscription.updated')) )::text`
	var raw string
	if e := f.app.DBPool().QueryRow(t.Context(), query).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	return raw
}
func m5CreationUnits(t *testing.T, f *m5IndexerAcceptance, media uuid.UUID, want []string) map[string]uuid.UUID {
	t.Helper()
	rows, e := f.app.DBPool().Query(t.Context(), `SELECT unit_key,id FROM media_unit WHERE media_id=$1 AND NOT retired ORDER BY unit_key`, media)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	keys := []string{}
	ids := map[string]uuid.UUID{}
	for rows.Next() {
		var k string
		var id uuid.UUID
		if e = rows.Scan(&k, &id); e != nil {
			t.Fatal(e)
		}
		keys = append(keys, k)
		ids[k] = id
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("actual active unit keys=%v, want=%v", keys, want)
	}
	return ids
}
func m5CreationRecover(t *testing.T, f *m5IndexerAcceptance, p *m5CreationProtocol, key string, body any, first m5IndexerHTTPResult) {
	t.Helper()
	// Move only derived evaluation due times; retain all actual intent/user facts.
	m5InteractiveSQL(t, f, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 day'`)
	calls := p.count()
	before := m5CreationSnapshot(t, f)
	for range 3 {
		replay := m5CreationPost(t, f, key, body)
		get := f.request(t, "GET", "/api/v1/subscription-commands/"+key, "", nil, f.session)
		m5IndexerStatus(t, get, 200)
		if !bytes.Equal(first.body, replay.body) || !bytes.Equal(first.body, get.body) {
			t.Fatal("lost-response replay/GET must preserve original byte-equivalent 201 snapshot")
		}
		if get.header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("recovery must be private/no-store")
		}
	}
	m5IndexerStatus(t, f.request(t, "GET", "/api/v1/subscription-commands/never-published", "", nil, f.session), 404)
	if p.count() != calls || m5CreationSnapshot(t, f) != before {
		t.Fatal("receipt recovery/unknown GET performed metadata I/O or mutated creation facts/ledgers")
	}
}

func TestM5SubscriptionCreationNativeSparseRecoveryHTTPPG(t *testing.T) {
	f, p := newM5CreationAcceptance(t)
	movieBody := map[string]any{"tmdb_id": 550, "title": "Untrusted ignored title", "year": 1901}
	// Eight actual Cookie HTTP requests share one command. Duplicate initial reads
	// are permitted; all publication and original response bytes must be unique.
	results := make([]m5IndexerHTTPResult, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		wg.Go(func() {
			<-start
			b, _ := json.Marshal(movieBody)
			req, e := http.NewRequest("POST", f.base+m5CreationPath, bytes.NewReader(b))
			if e != nil {
				errs <- e
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", f.base)
			req.Header.Set("Idempotency-Key", "eight-movie")
			req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.session})
			resp, e := (&http.Client{Timeout: 40 * time.Second}).Do(req)
			if e != nil {
				errs <- e
				return
			}
			defer func() { _ = resp.Body.Close() }()
			raw, e := io.ReadAll(resp.Body)
			if e != nil {
				errs <- e
				return
			}
			results[i] = m5IndexerHTTPResult{resp.StatusCode, raw, resp.Header}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	for _, r := range results {
		m5IndexerStatus(t, r, 201)
		if !bytes.Equal(results[0].body, r.body) {
			t.Fatal("concurrent same-key response changed")
		}
	}
	first := results[0]
	movie := m5CreationID(t, first, "media_id")
	movieSub := m5CreationID(t, first, "id")
	m5CreationUnits(t, f, movie, []string{"movie"})
	for _, q := range []string{`SELECT count(*) FROM media`, `SELECT count(*) FROM subscription`, `SELECT count(*) FROM wanted_item`, `SELECT count(*) FROM idempotency_record WHERE idempotency_key='subscription.creation:eight-movie'`, `SELECT count(*) FROM audit_log WHERE action='subscription.creation'`} {
		if n := m5InteractiveInt(t, f, q); n != 1 {
			t.Fatal("eight same-key publication count", q, n)
		}
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM media WHERE id=$1 AND title='Native trusted movie' AND year=1999 AND media_type='movie'`, movie) != 1 {
		t.Fatal("deprecated client title/year overrode provider facts")
	}
	m5CreationRecover(t, f, p, "eight-movie", movieBody, first)
	m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "eight-movie", map[string]any{"tmdb_id": 551}, f.session), 409)
	tvBody := map[string]any{"tmdb_id": 550, "media_type": "tv"}
	tvReceipt := m5CreationPost(t, f, "sparse-tv", tvBody)
	tv := m5CreationID(t, tvReceipt, "media_id")
	sub := m5CreationID(t, tvReceipt, "id")
	if tv == movie || sub == movieSub {
		t.Fatal("movie and TV numeric 550 collapsed")
	}
	keys := []string{"S01E01", "S01E03", "S02E01", "S02E03"}
	oldIDs := m5CreationUnits(t, f, tv, keys)
	if m5InteractiveInt(t, f, `SELECT count(*) FROM media_identity WHERE external_id='550' AND namespace IN ('tmdb.movie','tmdb.tv')`) != 2 {
		t.Fatal("typed TMDB namespaces missing")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM subscription s JOIN media m ON m.id=s.media_id JOIN library_root r ON r.id=s.root_id JOIN app_user a ON a.id=s.user_id WHERE m.media_type=r.media_type AND a.username='creation-admin' AND r.health='online' AND r.baseline_completed`) != 2 {
		t.Fatal("actual owner/type/root inference mismatch")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM media_unit u JOIN media_unit_identity i ON i.unit_id=u.id WHERE u.media_id=$1 AND i.namespace='tmdb.episode' AND i.external_id=5500000+u.season_number*100+u.episode_number AND i.tmdb_series_id=550`, tv) != 4 {
		t.Fatal("positive actual provider episode identities missing")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM media_unit WHERE media_id=$1 AND ((unit_key='S02E03' AND air_date IS NULL) OR (unit_key='S02E01' AND air_date='2099-01-01') OR (unit_key='S01E01' AND air_date='2020-01-01'))`, tv) != 3 {
		t.Fatal("native air dates/NULL changed")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1`, sub) != 4 || p.pathCount("/3/tv/550/season/0") != 0 || p.pathCount("/3/tv/550/season/3") != 0 {
		t.Fatal("Wanted missing or specials/zero-count season fetched implicitly")
	}
	m5CreationRecover(t, f, p, "sparse-tv", tvBody, tvReceipt)
	control := func(key string, body map[string]any) {
		body["expected_version"] = m5CreationVersion(t, f, sub)
		m5IndexerStatus(t, f.request(t, "PUT", m5CreationPath+"/"+sub.String()+"/control", key, body, f.session), 200)
	}
	control("skip-native", map[string]any{"action": "skip", "unit_id": oldIDs["S01E03"], "skipped": true})
	control("pause-native", map[string]any{"action": "state", "state": "paused"})
	version := m5CreationVersion(t, f, sub)
	metadataVersion := m5InteractiveInt(t, f, `SELECT metadata_version FROM media WHERE id=$1`, tv)
	m5CreationTV(p, 550, true, true, false)
	ensured := m5CreationPost(t, f, "ensure-tv-uuid", map[string]any{"media_id": tv})
	if m5CreationID(t, ensured, "id") != sub || m5CreationVersion(t, f, sub) != version {
		t.Fatal("equivalent TV UUID ensure bumped intent or changed subscription")
	}
	if m5InteractiveInt(t, f, `SELECT metadata_version FROM media WHERE id=$1`, tv) <= metadataVersion || m5InteractiveInt(t, f, `SELECT count(*) FROM tv_season_metadata_snapshot x JOIN media m ON m.id=x.media_id AND m.metadata_version=x.media_version WHERE m.id=$1 AND x.source_tmdb_id=550`, tv) != 3 {
		t.Fatal("native UUID ensure did not advance metadata version with three actual season proofs")
	}
	ids := m5CreationUnits(t, f, tv, []string{"S01E01", "S01E03", "S01E05", "S02E01", "S02E03", "S03E07"})
	for k, id := range oldIDs {
		if ids[k] != id {
			t.Fatal("metadata merge changed existing unit UUID", k)
		}
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2 AND user_skipped AND is_skipped`, sub, oldIDs["S01E03"]) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM subscription WHERE id=$1 AND state='paused'`, sub) != 1 {
		t.Fatal("ensure cleared user skip or resumed paused intent")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1`, sub) != 6 {
		t.Fatal("new native episode/season did not expand real Wanted")
	}
	// Historical result remains the first result after later metadata and controls.
	m5CreationRecover(t, f, p, "sparse-tv", tvBody, tvReceipt)
	changed := map[string]any{"media_id": tv, "include_specials": true}
	m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "specials-unfenced", changed, f.session), 409)
	changed["expected_intent_version"] = version
	m5CreationPost(t, f, "explicit-specials", changed)
	m5CreationUnits(t, f, tv, []string{"S00E02", "S01E01", "S01E03", "S01E05", "S02E01", "S02E03", "S03E07"})
	// Both the rejected unfenced change and the fenced change explicitly request specials.
	if p.pathCount("/3/tv/550/season/0") != 2 {
		t.Fatal("specials require explicit native observation")
	}
	m5CreationTV(p, 551, false, false, true)
	pending := m5CreationPost(t, f, "announced-empty", map[string]any{"tmdb_id": 551, "media_type": "tv"})
	m5CreationUnits(t, f, m5CreationID(t, pending, "media_id"), []string{})
	if m5InteractiveInt(t, f, `SELECT count(*) FROM wanted_item WHERE subscription_id=$1`, m5CreationID(t, pending, "id")) != 0 {
		t.Fatal("announced count-zero TV fabricated Wanted")
	}
	for _, q := range []string{`SELECT count(*) FROM download_task`, `SELECT count(*) FROM release_candidate`, `SELECT count(*) FROM library_file`} {
		if n := m5InteractiveInt(t, f, q); n != 0 {
			t.Fatal("creation without bindings granted external acquisition", q, n)
		}
	}
	t.Log("actual Cookie/App/PG/native TMDB: eight-key publication, separate movie/TV 550, sparse/future/NULL metadata, stable recovery, paused/skipped UUID ensure and zero-count TV verified; automatic TV acquisition remains outside this slice")
}

func TestM5SubscriptionCreationStrictValidationHTTPPG(t *testing.T) {
	f, p := newM5CreationAcceptance(t)
	before := m5CreationSnapshot(t, f)
	cases := []struct {
		name, key, body string
		status          int
	}{
		{"unknown", "invalid-unknown", `{"tmdb_id":550,"unknown":true}`, 400},
		{"missing-key", "", `{"tmdb_id":550}`, 400},
		{"bad-key", "invalid key", `{"tmdb_id":550}`, 400},
		{"identities", "invalid-identities", `{"tmdb_id":550,"media_id":"11111111-1111-4111-8111-111111111111"}`, 400},
		{"scope", "invalid-scope", `{"tmdb_id":550,"scope_type":"selected"}`, 400},
		{"movie-season", "invalid-season", `{"tmdb_id":550,"seasons":[1]}`, 400},
		{"special-implicit", "invalid-special", `{"tmdb_id":550,"media_type":"tv","seasons":[0]}`, 400},
		{"absent-media", "invalid-absent", `{"media_id":"11111111-1111-4111-8111-111111111111"}`, 404},
		{"absent-root", "invalid-root", `{"tmdb_id":550,"root_id":"11111111-1111-4111-8111-111111111111"}`, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, tc.key, tc.body, f.session), tc.status)
		})
	}
	var tvRoot uuid.UUID
	if e := f.app.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='tv'`).Scan(&tvRoot); e != nil {
		t.Fatal(e)
	}
	m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "wrong-root-type", map[string]any{"tmdb_id": 550, "root_id": tvRoot}, f.session), 400)
	m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "anonymous", map[string]any{"tmdb_id": 550}, ""), 401)
	if m5CreationSnapshot(t, f) != before {
		t.Fatal("invalid HTTP input partially published intent or media")
	}
	// Root health arrangement tests selection only; native baseline was established above.
	m5InteractiveSQL(t, f, `UPDATE library_root SET health='offline' WHERE media_type='movie'`)
	m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "no-online-root", map[string]any{"tmdb_id": 550}, f.session), 400)
	m5InteractiveSQL(t, f, `UPDATE library_root SET health='online' WHERE media_type='movie'`)
	offline := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO library_root(id,name,path,media_type,health) VALUES($1,'offline selection root',$2,'movie','offline')`, offline, filepath.Join(t.TempDir(), "offline"))
	// One online plus an offline configured root still infers the online root.
	created := m5CreationPost(t, f, "one-online-root", map[string]any{"tmdb_id": 550})
	if m5CreationID(t, created, "root_id") == offline {
		t.Fatal("inference selected offline configured root")
	}
	m5InteractiveSQL(t, f, `UPDATE library_root SET health='online' WHERE id=$1`, offline)
	m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "ambiguous-roots", map[string]any{"tmdb_id": 550}, f.session), 400)
	_ = p
}

func TestM5SubscriptionCreationNativeFailuresRollbackHTTPPG(t *testing.T) {
	f, p := newM5CreationAcceptance(t)
	for i, kind := range []string{"malformed-details", "wrong-details-id", "malformed-season", "wrong-season", "partial-season", "bad-date", "provider-episode-id", "too-many-seasons", "too-many-per-season", "too-many-total"} {
		t.Run(kind, func(t *testing.T) {
			id := 600 + i
			m5CreationTV(p, id, false, false, false)
			path := fmt.Sprintf("/3/tv/%d", id)
			switch kind {
			case "malformed-details":
				p.set(path, `{"id":`)
			case "wrong-details-id":
				p.set(path, fmt.Sprintf(`{"id":%d,"name":"wrong","seasons":[]}`, id+1))
			case "malformed-season":
				p.set(path+"/season/1", `{"season_number":1}`)
			case "wrong-season":
				p.set(path+"/season/1", `{"season_number":2,"episodes":[]}`)
			case "partial-season":
				p.set(path+"/season/1", `{"season_number":1,"episodes":[]}`)
			case "bad-date":
				p.set(path+"/season/1", `{"season_number":1,"episodes":[{"id":90000001,"season_number":1,"episode_number":1,"air_date":"2020-99-99"}]}`)
			case "provider-episode-id":
				p.set(path+"/season/1", `{"season_number":1,"episodes":[{"id":0,"season_number":1,"episode_number":1}]}`)
			default:
				seasons := []map[string]int{}
				n, count := 65, 0
				if kind == "too-many-per-season" {
					n, count = 1, 5001
				}
				if kind == "too-many-total" {
					n, count = 5, 5000
				}
				for sn := 1; sn <= n; sn++ {
					seasons = append(seasons, map[string]int{"season_number": sn, "episode_count": count})
				}
				p.set(path, m5CreationJSON(map[string]any{"id": id, "name": "bounded metadata", "seasons": seasons}))
			}
			before := m5CreationSnapshot(t, f)
			m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "failure-"+kind, map[string]any{"tmdb_id": id, "media_type": "tv"}, f.session), 503)
			if m5CreationSnapshot(t, f) != before {
				t.Fatal("native metadata failure left partial media/units/Wanted/command/audit")
			}
		})
	}
}

func TestM5SubscriptionCreationCurrentAccountFencesHTTPPG(t *testing.T) {
	f, p := newM5CreationAcceptance(t)
	member := uuid.New()
	other := uuid.New()
	for _, u := range []struct {
		id         uuid.UUID
		name, role string
	}{{member, "creation-member", "member"}, {other, "creation-other", "admin"}} {
		m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,$2,password_hash,$3,true FROM app_user WHERE username='creation-admin'`, u.id, u.name, u.role)
	}
	memberSession := m5CreationLogin(t, f, "creation-member")
	otherSession := m5CreationLogin(t, f, "creation-other")
	r := f.request(t, "POST", m5CreationPath, "member-original", map[string]any{"tmdb_id": 550}, memberSession)
	m5IndexerStatus(t, r, 201)
	sub := m5CreationID(t, r, "id")
	path := "/api/v1/subscription-commands/member-original"
	m5IndexerStatus(t, f.request(t, "GET", path, "", nil, otherSession), 404)
	m5InteractiveSQL(t, f, `UPDATE subscription SET user_id=$2 WHERE id=$1`, sub, other)
	denied := f.request(t, "GET", path, "", nil, memberSession)
	m5IndexerStatus(t, denied, 403)
	if bytes.Contains(denied.body, []byte(sub.String())) {
		t.Fatal("foreign current owner recovery leaked original receipt")
	}
	m5InteractiveSQL(t, f, `UPDATE subscription SET user_id=$2 WHERE id=$1`, sub, member)
	calls := p.count()
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=false WHERE id=$1`, member)
	m5IndexerStatus(t, f.request(t, "GET", path, "", nil, memberSession), 401)
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=true WHERE id=$1`, member)
	m5InteractiveSQL(t, f, `UPDATE app_session SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, member)
	m5IndexerStatus(t, f.request(t, "GET", path, "", nil, memberSession), 401)
	if p.count() != calls {
		t.Fatal("unauthorized recovery called native metadata")
	}
	// Both changes happen after the actual native provider is waiting and before
	// publication, proving the store rechecks the captured account/role under lock.
	for i, change := range []string{"disabled", "role-changed"} {
		t.Run(change, func(t *testing.T) {
			id := 700 + i
			m5CreationTV(p, id, false, false, false)
			entered, release := p.block(fmt.Sprintf("/3/tv/%d", id))
			defer release()
			before := m5CreationSnapshot(t, f)
			done := make(chan m5IndexerHTTPResult, 1)
			go func() {
				done <- f.request(t, "POST", m5CreationPath, "wait-"+change, map[string]any{"tmdb_id": id, "media_type": "tv"}, f.session)
			}()
			select {
			case <-entered:
			case <-time.After(15 * time.Second):
				t.Fatal("native metadata wait was not reached")
			}
			if change == "disabled" {
				m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=false WHERE username='creation-admin'`)
			} else {
				m5InteractiveSQL(t, f, `UPDATE app_user SET role='member' WHERE username='creation-admin'`)
			}
			release()
			select {
			case result := <-done:
				m5IndexerStatus(t, result, 403)
			case <-time.After(30 * time.Second):
				t.Fatal("native request did not join")
			}
			if m5CreationSnapshot(t, f) != before {
				t.Fatal("actor changed during native metadata wait but creation partially published")
			}
			m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=true,role='admin' WHERE username='creation-admin'`)
		})
	}
}

// A broken root read must remain a storage failure. Renaming this fixture's
// relation temporarily induces a real PG query error; no user facts are altered.
func TestM5SubscriptionCreationRootStorageFailureHTTPPG(t *testing.T) {
	f, p := newM5CreationAcceptance(t)
	before, calls := m5CreationSnapshot(t, f), p.count()
	m5InteractiveSQL(t, f, `ALTER TABLE library_root RENAME TO m5_creation_unavailable_root`)
	var result m5IndexerHTTPResult
	func() {
		defer m5InteractiveSQL(t, f, `ALTER TABLE m5_creation_unavailable_root RENAME TO library_root`)
		result = f.request(t, "POST", m5CreationPath, "root-storage-failure", map[string]any{"tmdb_id": 550}, f.session)
	}()
	m5IndexerStatus(t, result, 503)
	if p.count() != calls || m5CreationSnapshot(t, f) != before {
		t.Fatal("root storage failure was masked or partially published metadata/intent")
	}
}
