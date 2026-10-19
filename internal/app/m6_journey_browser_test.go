package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	searchusecase "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
)

const m6JourneyTitle = "Native Automatic Series"
const m6JourneyRelease = "Native.Automatic.Series.2020.S01E03.1080p.WEB-DL.x264"

// Minimal normal-path protocol fixture copied from M5's external httpapi_test
// package. Only native arrival is synthetic; App.Run owns every business write.
type m6Journey struct {
	application                                   *App
	base, session, rootPath                       string
	root, client, indexer                         uuid.UUID
	raw                                           []byte
	hash                                          string
	names                                         []string
	payloads                                      [][]byte
	total                                         int64
	mu                                            sync.Mutex
	save, tags                                    string
	operation                                     uuid.UUID
	searches, fetches, adds, infoReads, fileReads atomic.Int32
	complete                                      atomic.Bool
}

func m6JourneyTorrent() ([]byte, string, []string, [][]byte) {
	names := []string{m6JourneyRelease + ".mkv", m6JourneyRelease + ".en.srt", "unwanted.txt"}
	payloads := [][]byte{bytes.Repeat([]byte("Synthetic private television acceptance frame.\n"), 97), []byte("1\n00:00:00,000 --> 00:00:01,000\nSynthetic subtitle\n"), {}}
	var info bytes.Buffer
	info.WriteString("d5:filesl")
	for i, name := range names {
		fmt.Fprintf(&info, "d6:lengthi%de4:pathl%d:%see", len(payloads[i]), len(name), name)
	}
	fmt.Fprintf(&info, "e4:name%d:%s12:piece lengthi16384e6:pieces", len(m6JourneyRelease), m6JourneyRelease)
	all := bytes.Join(payloads, nil)
	var pieces []byte
	for start := 0; start < len(all); start += 16384 {
		sum := sha1.Sum(all[start:min(start+16384, len(all))])
		pieces = append(pieces, sum[:]...)
	}
	fmt.Fprintf(&info, "%d:", len(pieces))
	info.Write(pieces)
	info.WriteString("7:privatei1ee")
	sum := sha1.Sum(info.Bytes())
	var torrent bytes.Buffer
	const tracker = "http://127.0.0.1:1/announce"
	fmt.Fprintf(&torrent, "d8:announce%d:%s4:info", len(tracker), tracker)
	torrent.Write(info.Bytes())
	torrent.WriteByte('e')
	return torrent.Bytes(), hex.EncodeToString(sum[:]), names, payloads
}

func (h *m6Journey) serveSource(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if r.URL.Path == "/torrent" {
		if r.Method != "GET" || r.URL.RawQuery != "" {
			t.Error("original torrent GET changed")
			w.WriteHeader(400)
			return
		}
		h.fetches.Add(1)
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(h.raw)
		return
	}
	q := r.URL.Query()
	if r.URL.Path != "/api" || r.Method != "GET" || q.Get("apikey") != "owned-tv-source-key" {
		t.Error("native source configuration changed")
		w.WriteHeader(400)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	if q.Get("t") == "caps" {
		_, _ = io.WriteString(w, `<caps><limits default="50" max="100"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q"/><tv-search available="yes" supportedParams="q,season,ep"/></searching></caps>`)
		return
	}
	// Health probes never expose a candidate. Only the exact episode request
	// may produce the privately generated torrent.
	if q.Get("t") != "tvsearch" || q.Get("season") == "" || q.Get("ep") == "" {
		_, _ = io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="0"/></channel></rss>`)
		return
	}
	h.searches.Add(1)
	if q.Get("q") != m6JourneyTitle+" 2020" || q.Get("season") != "1" || q.Get("ep") != "3" || q.Get("cat") != "5000" || q.Get("limit") != "50" || q.Get("offset") != "0" {
		t.Error("automatic TV native query lost q/season/ep/categories/paging or searched an ineligible unit")
		w.WriteHeader(400)
		return
	}
	_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="1"/><item><title>%s</title><guid>owned-automatic-tv</guid><enclosure url="http://%s/torrent" length="%d" type="application/x-bittorrent"/><torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="42"/><torznab:attr name="peers" value="1"/></item></channel></rss>`, m6JourneyRelease, r.Host, h.total, h.hash)
}

func (h *m6Journey) serveQB(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if r.URL.Path == "/api/v2/auth/login" {
		if r.Method != "POST" || r.ParseForm() != nil || r.FormValue("username") != "owned-tv" || r.FormValue("password") != "owned-tv-client-key" {
			t.Error("qB native authentication binding changed")
			w.WriteHeader(401)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "owned-tv-protocol", Path: "/"})
		_, _ = io.WriteString(w, "Ok.")
		return
	}
	cookie, err := r.Cookie("SID")
	if err != nil || cookie.Value != "owned-tv-protocol" {
		t.Error("qB request lost native session")
		w.WriteHeader(403)
		return
	}
	switch r.URL.Path {
	case "/api/v2/torrents/add":
		if h.adds.Add(1) != 1 {
			t.Error("automatic TV replayed physical Add")
			w.WriteHeader(409)
			return
		}
		if r.Method != "POST" || r.ParseMultipartForm(1<<20) != nil {
			t.Error("native multipart required")
			w.WriteHeader(400)
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				if e := r.MultipartForm.RemoveAll(); e != nil {
					t.Error(e)
				}
			}
		}()
		file, _, e := r.FormFile("torrents")
		if e != nil {
			t.Error("missing original torrent bytes", e)
			w.WriteHeader(400)
			return
		}
		raw, e := io.ReadAll(file)
		_ = file.Close()
		save, tags := r.FormValue("savepath"), r.FormValue("tags")
		prefix := "pulsemedia,pulsemedia:op:"
		op, parseErr := uuid.Parse(strings.TrimPrefix(tags, prefix))
		if e != nil || !bytes.Equal(raw, h.raw) || r.FormValue("urls") != "" || save != h.rootPath || !strings.HasPrefix(tags, prefix) || parseErr != nil || op == uuid.Nil || tags != prefix+op.String() {
			t.Error("native Add changed exact original bytes/hash/path/operation tags")
			w.WriteHeader(400)
			return
		}
		// This simulates backend arrival under the validated test-owned save path.
		// One video is linked later; subtitles and the unwanted file stay at source.
		dir := filepath.Join(save, m6JourneyRelease)
		if e = os.Mkdir(dir, 0700); e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		for i, name := range h.names {
			if e = os.WriteFile(filepath.Join(dir, name), h.payloads[i], 0600); e != nil {
				t.Error(e)
				w.WriteHeader(500)
				return
			}
		}
		h.mu.Lock()
		h.save, h.tags, h.operation = save, tags, op
		h.mu.Unlock()
		h.complete.Store(true)
		_, _ = fmt.Fprintf(w, `{"added_torrent_ids":["%s"],"failure_count":0,"pending_count":0,"success_count":1}`, h.hash)
	case "/api/v2/torrents/info":
		if r.Method != "GET" {
			t.Error("observation attempted mutation")
			w.WriteHeader(400)
			return
		}
		h.infoReads.Add(1)
		h.mu.Lock()
		save, tags := h.save, h.tags
		h.mu.Unlock()
		if save == "" {
			_, _ = io.WriteString(w, "[]")
			return
		}
		if hash := r.URL.Query().Get("hashes"); hash != "" && hash != h.hash {
			t.Error("observation changed original hash")
			w.WriteHeader(400)
			return
		}
		if tag := r.URL.Query().Get("tag"); tag != "" && tag != strings.TrimPrefix(tags, "pulsemedia,") {
			t.Error("observation changed original operation tag")
			w.WriteHeader(400)
			return
		}
		state, completed, progress := "downloading", int64(0), 0
		if h.complete.Load() {
			state, completed, progress = "uploading", h.total, 1
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": h.hash, "name": m6JourneyRelease, "save_path": save, "content_path": filepath.Join(save, m6JourneyRelease), "state": state, "tags": tags, "total_size": h.total, "completed": completed, "size": h.total, "downloaded": completed, "progress": progress}})
	case "/api/v2/torrents/files":
		if r.Method != "GET" || r.URL.Query().Get("hash") != h.hash {
			t.Error("inventory lost original hash")
			w.WriteHeader(400)
			return
		}
		h.fileReads.Add(1)
		rows := make([]map[string]any, len(h.names))
		for i, name := range h.names {
			priority := 1
			if i == len(h.names)-1 {
				priority = 0
			}
			rows[i] = map[string]any{"index": i, "name": m6JourneyRelease + "/" + name, "size": len(h.payloads[i]), "priority": priority, "progress": 1}
		}
		_ = json.NewEncoder(w).Encode(rows)
	default:
		t.Errorf("unexpected qB endpoint %s", r.URL.Path)
		w.WriteHeader(404)
	}
}

type m6JourneyMetadataRoute struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r m6JourneyMetadataRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.themoviedb.org" || !strings.HasPrefix(req.URL.Path, "/3/") {
		return nil, errors.New("unexpected metadata destination")
	}
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.target.Scheme, r.target.Host
	clone.URL, clone.Host = &u, r.target.Host
	return r.transport.RoundTrip(clone)
}

func (h *m6Journey) request(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, h.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.base)
	req.Header.Set("Idempotency-Key", uuid.NewString())
	if h.session != "" {
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: h.session})
	}
	res, err := (&http.Client{Timeout: 35 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("bootstrap request", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != status {
		t.Fatalf("bootstrap %s %s status=%d", method, path, res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == "pulsemedia_session" {
			h.session = c.Value
		}
	}
	var out map[string]any
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func m6JourneyWait(t *testing.T, budget time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("journey readiness deadline")
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestM6JourneyActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M6_JOURNEY_BROWSER") != "1" {
		t.Skip("explicit Main-owned PG18.6 and Chrome runtime required")
	}
	assets, shots := os.Getenv("PULSE_M6_JOURNEY_ASSETS"), os.Getenv("PULSE_M6_JOURNEY_SCREENSHOTS")
	for _, p := range []string{assets, shots, os.TempDir()} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/data/temp/") {
			t.Fatal("owned paths required")
		}
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	h := &m6Journey{}
	h.raw, h.hash, h.names, h.payloads = m6JourneyTorrent()
	for _, b := range h.payloads {
		h.total += int64(len(b))
	}
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveQB(t, w, r) }))
	t.Cleanup(qb.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveSource(t, w, r) }))
	t.Cleanup(source.Close)
	var metadataCalls atomic.Int32
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("api_key") != "owned-journey-tmdb" || r.URL.Query().Get("language") != "zh-CN" {
			t.Error("metadata credential/locale changed")
			http.Error(w, "metadata", 400)
			return
		}
		metadataCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		detail := `{"id":550,"media_type":"tv","name":"Native Automatic Series","original_name":"Native Automatic Series","first_air_date":"2020-01-01","overview":"Private native journey metadata","poster_path":"/Journey.jpg","vote_average":8.1,"seasons":[{"season_number":1,"episode_count":3},{"season_number":2,"episode_count":1}]}`
		switch r.URL.Path {
		case "/3/trending/all/day":
			_, _ = io.WriteString(w, `{"page":1,"total_pages":1,"total_results":1,"results":[`+detail+`]}`)
		case "/3/tv/550":
			_, _ = io.WriteString(w, detail)
		case "/3/tv/550/credits":
			_, _ = io.WriteString(w, `{"id":550,"cast":[]}`)
		case "/3/tv/550/season/1":
			_, _ = io.WriteString(w, `{"id":5501,"season_number":1,"episodes":[{"id":550101,"name":"Future","season_number":1,"episode_number":1,"air_date":"2099-01-01"},{"id":550103,"name":"Actual sparse episode","season_number":1,"episode_number":3,"air_date":"2020-01-15"},{"id":550107,"name":"Unknown date","season_number":1,"episode_number":7,"air_date":null}]}`)
		case "/3/tv/550/season/2":
			_, _ = io.WriteString(w, `{"id":5502,"season_number":2,"episodes":[{"id":550209,"season_number":2,"episode_number":9,"air_date":null}]}`)
		default:
			t.Errorf("unexpected metadata path %s", r.URL.Path)
			http.Error(w, "metadata", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(metadata.Close)
	target, err := url.Parse(metadata.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err = postgres.Migrate(t.Context(), dsn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
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
	h.application, err = New(cfg, WithMetadataHTTPClient(&http.Client{Transport: m6JourneyMetadataRoute{target, tr}, Timeout: 10 * time.Second}), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	h.base = "http://" + cfg.Server.HTTPAddr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.application.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(50 * time.Second):
			t.Error("App shutdown budget exceeded")
		}
		if e := h.application.Close(); e != nil {
			t.Error(e)
		}
	})
	m6JourneyWait(t, 15*time.Second, func() bool {
		r, e := (&http.Client{Timeout: time.Second}).Get(h.base + "/api/v1/auth/status")
		if e != nil {
			return false
		}
		defer func() { _ = r.Body.Close() }()
		return r.StatusCode == 200
	})
	var version int
	if err = h.application.DBPool().QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version != 180006 {
		t.Fatal("PG18.6 required", err)
	}
	h.request(t, "POST", "/api/v1/auth/setup", map[string]string{"username": "journey-admin", "password": "Journey-Owned-123!"}, 200)
	if h.session == "" {
		t.Fatal("setup Cookie missing")
	}
	h.request(t, "PUT", "/api/v1/settings/tmdb.api_key", map[string]any{"expected_version": 0, "value": map[string]string{"api_key": "owned-journey-tmdb"}}, 200)
	m6JourneyWait(t, 25*time.Second, func() bool {
		return h.application.DBPool().QueryRow(t.Context(), `SELECT id,path FROM library_root WHERE media_type='tv' AND health='online' AND baseline_completed`).Scan(&h.root, &h.rootPath) == nil
	})
	if !strings.HasPrefix(h.rootPath, os.TempDir()+string(os.PathSeparator)) {
		t.Fatal("root escaped owned temporary directory")
	}
	if _, err = os.Stat(filepath.Join(h.rootPath, ".pulsemedia_root")); err != nil {
		t.Fatal(err)
	}
	idx := h.request(t, "POST", "/api/v1/indexers", map[string]any{"name": "Journey source", "protocol": "torznab", "url": source.URL, "api_key": "owned-tv-source-key", "categories": []int{5000}}, 201)
	h.indexer = uuid.MustParse(idx["id"].(string))
	probe := h.request(t, "POST", "/api/v1/indexers/"+h.indexer.String()+"/test", nil, 200)
	if probe["alive"] != true {
		t.Fatal("source probe failed")
	}
	dl := h.request(t, "POST", "/api/v1/download-clients", map[string]any{"name": "Journey qB", "client_type": "qbittorrent", "url": qb.URL, "username": "owned-tv", "password": "owned-tv-client-key", "remote_path_map": map[string]string{}}, 201)
	h.client = uuid.MustParse(dl["id"].(string))
	policies := h.request(t, "GET", "/api/v1/policies", nil, 200)
	items := policies["items"].([]any)
	if len(items) == 0 {
		t.Fatal("native policy missing")
	}
	h.request(t, "POST", "/api/v1/policies", map[string]any{"id": "m6-journey", "name": "Journey policy", "rules": items[0].(map[string]any)["rules"]}, 201)
	upstream, _ := url.Parse(h.base)
	proxy := &httputil.ReverseProxy{FlushInterval: -1, Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(upstream)
		pr.Out.Host = upstream.Host
		if pr.In.Header.Get("Origin") == "http://"+pr.In.Host {
			pr.Out.Header.Set("Origin", h.base)
		}
	}}
	files := http.FileServer(http.Dir(assets))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			proxy.ServeHTTP(w, r)
		case r.URL.Path == "/__m6_journey/evidence":
			if r.Method != "GET" {
				http.Error(w, "read only", http.StatusMethodNotAllowed)
				return
			}
			snapshot, e := h.snapshot(r.Context())
			if e != nil {
				t.Error(e)
				http.Error(w, "evidence", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, snapshot)
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			files.ServeHTTP(w, r)
		default:
			http.ServeFile(w, r, filepath.Join(assets, "index.html"))
		}
	}))
	t.Cleanup(bridge.Close)
	browserCtx, stop := context.WithTimeout(t.Context(), 220*time.Second)
	defer stop()
	cdp := startOwnedRevisionChrome(t, browserCtx)
	cmd := exec.CommandContext(browserCtx, "node", "tests/m6-journey-browser.mjs")
	cmd.Dir = "../../web"
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M6_JOURNEY_BASE="+bridge.URL, "PULSE_M6_JOURNEY_SESSION="+h.session, "PULSE_M6_JOURNEY_CDP="+cdp, "PULSE_M6_JOURNEY_ROOT="+h.root.String(), "PULSE_M6_JOURNEY_CLIENT="+h.client.String(), "PULSE_M6_JOURNEY_INDEXER="+h.indexer.String())
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("actual Chrome journey: %v\n%s", e, output)
	}
	if !strings.Contains(string(output), "M6_JOURNEY_BROWSER_PASS") {
		t.Fatal("missing browser receipt")
	}
	h.assertImported(t)
	if metadataCalls.Load() < 4 {
		t.Fatal("native metadata path not exercised")
	}
	t.Log(strings.TrimSpace(string(output)))
	t.Log("M6_JOURNEY_APP_COOKIE_PG_REALFS_PASS synthetic native arrival; production automatic finalizer")
}

// The evidence endpoint performs SELECTs only. Stable completion rows make it
// possible to compare before/after actual UI read GETs without worker clocks.
func (h *m6Journey) snapshot(ctx context.Context) (string, error) {
	var out string
	err := h.application.DBPool().QueryRow(ctx, `SELECT jsonb_build_object(
 'subscriptions',(SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'media_id',media_id,'root_id',root_id,'downloader_id',downloader_id,'policy',quality_policy_id) ORDER BY id),'[]') FROM subscription),
 'wanted',(SELECT coalesce(jsonb_agg(jsonb_build_object('key',u.unit_key,'excluded',w.scope_excluded,'skipped',w.is_skipped,'satisfied',w.is_satisfied,'stage',w.current_fact_stage) ORDER BY u.unit_key),'[]') FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id),
 'facts',(SELECT count(*) FROM unit_fact WHERE stage='imported' AND revoked_at IS NULL),
 'maps',(SELECT count(*) FROM library_file_unit),
 'operations',(SELECT count(DISTINCT operation_id) FROM download_requirement),
 'tasks',(SELECT count(*) FROM download_task),
 'completed_tasks',(SELECT count(*) FROM download_task WHERE state='completed' AND organization_state='completed'),
 'submission_jobs',(SELECT count(*) FROM job WHERE kind='automatic_tv_download_submission'),
 'authorities',(SELECT count(*) FROM automatic_tv_execution_authority),
 'plans',(SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'operation_id',operation_id,'state',state) ORDER BY id),'[]') FROM transfer_plan),
 'steps',(SELECT count(*) FROM transfer_step WHERE state='completed'),
 'organization_jobs',(SELECT count(*) FROM job WHERE kind='download_organization'),
 'organization_succeeded',(SELECT count(*) FROM job WHERE kind='download_organization' AND state='succeeded'),
 'imports',(SELECT count(*) FROM outbox_event WHERE event_type='media.imported'),
 'creation_receipts',(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'subscription.creation:%'),
 'files',(SELECT coalesce(jsonb_agg(jsonb_build_object('file_id',f.id,'unit_id',m.unit_id,'key',u.unit_key,'path',f.rel_path) ORDER BY f.id,m.unit_id),'[]') FROM library_file f JOIN library_file_unit m ON m.file_id=f.id JOIN media_unit u ON u.id=m.unit_id WHERE f.retired_at IS NULL)
 )::text`).Scan(&out)
	return out, err
}

func (h *m6Journey) assertImported(t *testing.T) {
	t.Helper()
	pool := h.application.DBPool()
	ctx := t.Context()
	if h.adds.Load() != 1 || h.searches.Load() != 1 || h.fetches.Load() != 1 || h.fileReads.Load() < 1 || h.infoReads.Load() < 3 {
		t.Fatalf("native counts Add=%d tvsearch=%d fetch=%d files=%d info=%d", h.adds.Load(), h.searches.Load(), h.fetches.Load(), h.fileReads.Load(), h.infoReads.Load())
	}
	var target string
	var unit uuid.UUID
	if e := pool.QueryRow(ctx, `SELECT f.rel_path,m.unit_id FROM library_file f JOIN library_file_unit m ON m.file_id=f.id WHERE f.root_id=$1 AND f.retired_at IS NULL`, h.root).Scan(&target, &unit); e != nil {
		t.Fatal(e)
	}
	if filepath.IsAbs(target) || strings.HasPrefix(target, "..") || !strings.Contains(target, "Season 01") || !strings.Contains(target, "S01E03") {
		t.Fatal("canonical target path", target)
	}
	source := filepath.Join(h.rootPath, m6JourneyRelease, h.names[0])
	dest := filepath.Join(h.rootPath, target)
	a, e := os.Stat(source)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.Stat(dest)
	if e != nil {
		t.Fatal(e)
	}
	if source == dest || !os.SameFile(a, b) {
		t.Fatal("production hardlink inode mismatch")
	}
	for i, name := range h.names {
		got, e := os.ReadFile(filepath.Join(h.rootPath, m6JourneyRelease, name))
		if e != nil || !bytes.Equal(got, h.payloads[i]) {
			t.Fatal("source/attachment preservation", e)
		}
	}
	got, e := os.ReadFile(dest)
	if e != nil || !bytes.Equal(got, h.payloads[0]) {
		t.Fatal("target bytes", e)
	}
	digest := sha256.Sum256(h.raw)
	h.mu.Lock()
	op := h.operation
	h.mu.Unlock()
	var statement []byte
	if e := pool.QueryRow(ctx, `SELECT statement FROM automatic_tv_observation_result WHERE operation_id=$1 AND state='completed'`, op).Scan(&statement); e != nil {
		t.Fatal(e)
	}
	var observed struct {
		Before, After []ports.AutomaticTVRemoteTask
		Files         []ports.AutomaticTVRemoteFile
	}
	if json.Unmarshal(statement, &observed) != nil || len(observed.Before) != 1 || len(observed.After) != 1 || len(observed.Files) != len(h.names) {
		t.Fatal("immutable A/files/B evidence missing")
	}
	for _, task := range append(observed.Before, observed.After...) {
		if task.InfoHash != h.hash || task.SavePath != h.rootPath || task.TotalBytes != h.total || task.VerifiedBytes != h.total || !task.Complete {
			t.Fatal("immutable native task totals changed")
		}
	}
	for i, file := range observed.Files {
		if file.Index != i || file.Path != m6JourneyRelease+"/"+h.names[i] || file.Size != int64(len(h.payloads[i])) || file.CompletedBytes != file.Size || file.Wanted != (i < len(h.names)-1) {
			t.Fatal("immutable full inventory changed")
		}
	}
	_, canonical, queryDigest, err := searchusecase.CanonicalTVEpisodeQuery(ports.TVEpisodeQuery{SeriesID: 550, Keyword: m6JourneyTitle + " 2020", Season: 1, Episode: 3, Categories: []int32{5000}, Offset: 0, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		query string
		args  []any
		want  int
	}{
		{`SELECT count(*) FROM automatic_tv_submission_preparation WHERE operation_id=$1 AND info_hash=$2 AND torrent_sha256=$3`, []any{op, h.hash, digest[:]}, 1},
		{`SELECT count(*) FROM automatic_tv_execution_authority WHERE operation_id=$1 AND unit_id=$2 AND root_id=$3 AND client_id=$4 AND snapshot->'target'->>'mapping_mode'='standard'`, []any{op, unit, h.root, h.client}, 1},
		{`SELECT count(*) FROM automatic_tv_observation_result WHERE operation_id=$1 AND state='completed' AND complete AND jsonb_array_length(inventory)=3`, []any{op}, 1},
		{`SELECT count(*) FROM unit_fact WHERE unit_id=$1 AND root_id=$2 AND stage='imported' AND revoked_at IS NULL`, []any{unit, h.root}, 1},
		{`SELECT count(*) FROM unit_reservation WHERE unit_id=$1 AND root_id=$2 AND state='consumed'`, []any{unit, h.root}, 1},
		{`SELECT count(*) FROM download_requirement WHERE unit_id=$1 AND root_id=$2 AND state='imported'`, []any{unit, h.root}, 1},
		{`SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL`, []any{op}, 0},
		{`SELECT count(*) FROM episode_candidate_review`, nil, 0},
		{`SELECT count(*) FROM automatic_tv_query q JOIN indexer_query i ON i.id=q.query_id WHERE i.query_type='tv_episode' AND i.state='succeeded' AND q.parameters=$1::jsonb AND q.parameters_hash=$2 AND q.page=0`, []any{string(canonical), queryDigest}, 1},
	}
	for _, c := range checks {
		var n int
		if e := pool.QueryRow(ctx, c.query, c.args...).Scan(&n); e != nil || n != c.want {
			t.Fatalf("final graph check want=%d got=%d: %s: %v", c.want, n, c.query, e)
		}
	}
}
