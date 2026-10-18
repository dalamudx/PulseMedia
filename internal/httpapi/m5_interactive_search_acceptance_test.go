package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests deliberately use the shared real App/Cookie/PG18.6 fixture.
// Run with PULSE_M5_INDEXER_ACCEPTANCE=1 through with_podman_test_postgres.sh.
const m5InteractivePath = "/api/v1/release-searches"

type m5InteractiveSource struct {
	id                                 string
	server                             *httptest.Server
	entered                            chan struct{}
	release                            chan struct{}
	returned                           chan struct{}
	enterOnce, releaseOnce, returnOnce sync.Once
	calls                              atomic.Int32
	blocked                            atomic.Bool
	cancelled                          atomic.Bool
	mu                                 sync.Mutex
	offsets                            []int
}

// Native requests have full URL userinfo, query credentials and explicit protocol.
// Holding the handler proves progressive visibility only; the native client itself
// honors cancellation. It is not an ignore-cancellation physical-I/O simulation.
func m5InteractiveNative(t *testing.T, f *m5IndexerAcceptance, name string, total int, hold bool, before <-chan struct{}) *m5InteractiveSource {
	t.Helper()
	s := &m5InteractiveSource{entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	if !hold {
		s.unblock()
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		user, pass, ok := r.BasicAuth()
		q := r.URL.Query()
		if r.URL.Path != "/m5-secret-path/api" || !ok || user != "m5-secret-user" || pass != "m5-secret-pass" || q.Get("token") != "m5-query-marker" || q.Get("apikey") != "m5-fixture=credential" || q.Get("t") != "movie" || q.Get("q") == "" || q.Get("limit") != "50" {
			t.Error("native interactive Torznab request lost explicit protocol, query, paging or full credentials")
			w.WriteHeader(400)
			return
		}
		offset := 0
		if text := q.Get("offset"); text != "" {
			var err error
			offset, err = strconv.Atoi(text)
			if err != nil || offset < 0 || offset%50 != 0 {
				t.Error("invalid native page offset")
				w.WriteHeader(400)
				return
			}
		}
		s.mu.Lock()
		s.offsets = append(s.offsets, offset)
		s.mu.Unlock()
		s.blocked.Store(hold)
		s.enterOnce.Do(func() { close(s.entered) })
		defer s.returnOnce.Do(func() { close(s.returned) })
		if before != nil {
			select {
			case <-before:
			case <-r.Context().Done():
				return
			}
		}
		select {
		case <-s.release:
		case <-r.Context().Done():
			s.cancelled.Store(true)
			s.blocked.Store(false)
			return
		}
		s.blocked.Store(false)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="%d" total="%d"/>`, offset, total)
		end := min(offset+50, total)
		for i := offset; i < end; i++ {
			// Reverse seeder order, repeated ties, and a low-quality/high-seeder row
			// make native order distinguishable from the required global ranking.
			profile := "2160p.BluRay.REMUX.HEVC.TrueHD.7.1-GROUP"
			seeders := i%7 + 1
			if i == 0 {
				profile = "720p.WEB-DL.H264.AAC-GROUP"
				seeders = 9999
			}
			_, _ = fmt.Fprintf(w, `<item><title>Interactive.Movie.2026.%s</title><guid>%s-%03d</guid><enclosure url="%s/download/%d?passkey=m5-download-only-secret" length="123456789" type="application/x-bittorrent"/><torznab:attr name="seeders" value="%d"/><torznab:attr name="peers" value="2"/></item>`, profile, name, i, s.server.URL, i, seeders)
		}
		_, _ = io.WriteString(w, `</channel></rss>`)
	}))
	// Registered after App cleanup: release and join every handler before App join.
	t.Cleanup(func() { s.unblock(); s.server.Close() })
	raw := strings.Replace(s.server.URL, "http://", "http://m5-secret-user:m5-secret-pass@", 1) + "/m5-secret-path?token=m5-query-marker"
	result := f.request(t, "POST", "/api/v1/indexers", "native-create-"+name, map[string]any{"name": name, "protocol": "torznab", "url": raw, "api_key": "m5-fixture=credential", "categories": []int{2000}}, f.session)
	m5IndexerStatus(t, result, 201)
	s.id = m5InteractiveString(t, m5IndexerObject(t, result), "id")
	return s
}
func (s *m5InteractiveSource) unblock() { s.releaseOnce.Do(func() { close(s.release) }) }
func m5InteractiveAwait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("deadline waiting for %s", what)
	}
}
func m5InteractiveString(t *testing.T, v map[string]any, key string) string {
	t.Helper()
	s, ok := v[key].(string)
	if !ok || s == "" {
		t.Fatalf("missing string %s: %v", key, v)
	}
	return s
}
func m5InteractiveCreate(t *testing.T, f *m5IndexerAcceptance, key string, body any) m5IndexerHTTPResult {
	t.Helper()
	r := f.request(t, "POST", m5InteractivePath, key, body, f.session)
	m5IndexerStatus(t, r, 201)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("search receipt missing no-store")
	}
	return r
}
func m5InteractiveGet(t *testing.T, f *m5IndexerAcceptance, id string) map[string]any {
	t.Helper()
	r := f.request(t, "GET", m5InteractivePath+"/"+id, "", nil, f.session)
	m5IndexerStatus(t, r, 200)
	if r.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal("snapshot missing no-store")
	}
	if bytes.Contains(r.body, []byte("m5-download-only-secret")) {
		t.Fatal("candidate download credential leaked")
	}
	v := m5IndexerObject(t, r)
	for _, field := range []string{"search", "state", "sources", "results", "partial", "truncated", "total_candidates", "statistics_mode", "sort_order"} {
		if _, present := v[field]; !present {
			t.Fatalf("snapshot missing %s", field)
		}
	}
	header, ok := v["search"].(map[string]any)
	if !ok || header["id"] != id || v["statistics_mode"] != "latest_persisted" || v["sort_order"] != "quality_desc_seeders_desc_id_asc" {
		t.Fatal("snapshot header/statistics/sort contract invalid", v)
	}
	for _, raw := range m5InteractiveRows(t, v, "sources") {
		source := raw.(map[string]any)
		switch source["state"] {
		case "queued", "running", "succeeded", "failed", "deferred", "timeout", "cancelled", "stale":
		default:
			t.Fatalf("unknown source state %v", source["state"])
		}
		m5InteractiveRows(t, source, "query_ids")
	}
	m5InteractiveRows(t, v, "results")
	return v
}
func m5InteractiveRows(t *testing.T, v map[string]any, key string) []any {
	t.Helper()
	r, ok := v[key].([]any)
	if !ok {
		t.Fatalf("%s must be a non-null array: %v", key, v)
	}
	return r
}
func m5InteractiveSourceView(t *testing.T, v map[string]any, id string) map[string]any {
	t.Helper()
	for _, raw := range m5InteractiveRows(t, v, "sources") {
		row := raw.(map[string]any)
		if row["indexer_id"] == id {
			return row
		}
	}
	t.Fatalf("source %s absent", id)
	return nil
}
func m5InteractiveWaitSnapshot(t *testing.T, f *m5IndexerAcceptance, id string, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		v := m5InteractiveGet(t, f, id)
		if ready(v) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("search snapshot deadline: %v", v)
		}
		time.Sleep(15 * time.Millisecond)
	}
}
func m5InteractiveTerminal(v map[string]any) bool {
	return v["state"] != "queued" && v["state"] != "running"
}
func m5InteractiveSQL(t *testing.T, f *m5IndexerAcceptance, sql string, args ...any) {
	t.Helper()
	if _, err := f.app.DBPool().Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func m5InteractiveInt(t *testing.T, f *m5IndexerAcceptance, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.app.DBPool().QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Freeze relevant rows in one SQL snapshot, excluding unrelated worker heartbeat,
// outbox delivery and session touch rows. Called only after source jobs quiesce.
func m5InteractiveReadFingerprint(t *testing.T, f *m5IndexerAcceptance) string {
	t.Helper()
	const sql = `SELECT jsonb_build_object(
 'search',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM interactive_search x),
 'source',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY search_id,indexer_id),'[]') FROM interactive_search_source x),
 'link',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY query_id),'[]') FROM interactive_search_query x),
 'query',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM indexer_query x),
 'candidate',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM release_candidate x),
 'audit',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM audit_log x),
 'event',(SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'type',event_type,'payload',payload) ORDER BY id),'[]') FROM outbox_event),
 'command',(SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY idempotency_key),'[]') FROM idempotency_record x)
 )::text`
	var result string
	if err := f.app.DBPool().QueryRow(t.Context(), sql).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
func m5InteractiveQuiesce(t *testing.T, f *m5IndexerAcceptance) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if m5InteractiveInt(t, f, `SELECT count(*) FROM job WHERE kind='interactive_search_source' AND state IN ('queued','running')`) == 0 {
			// Cancellation may retain a fenced running query as durable history.
			// Require the real native endpoint permits to have returned instead.
			// Try-only transaction locks neither wait nor release another owner's gate.
			tx, err := f.app.DBPool().Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var free bool
			err = tx.QueryRow(t.Context(), `SELECT COALESCE(bool_and(pg_try_advisory_xact_lock(hashtextextended('indexer:' || id::text,0))),true) FROM indexer`).Scan(&free)
			rollbackErr := tx.Rollback(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if free {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("interactive jobs/native endpoint permits did not quiesce")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func m5InteractivePureReads(t *testing.T, f *m5IndexerAcceptance, id, key string, sources ...*m5InteractiveSource) {
	t.Helper()
	m5InteractiveQuiesce(t, f)
	before := m5InteractiveReadFingerprint(t, f)
	var calls int32
	for _, s := range sources {
		calls += s.calls.Load()
	}
	for range 4 {
		m5InteractiveGet(t, f, id)
		m5IndexerStatus(t, f.request(t, "GET", "/api/v1/release-search-commands/"+key+"?action=create", "", nil, f.session), 200)
	}
	if after := m5InteractiveReadFingerprint(t, f); after != before {
		t.Fatal("GET mutated search/query/candidate/audit/outbox/command rows")
	}
	var afterCalls int32
	for _, s := range sources {
		afterCalls += s.calls.Load()
	}
	if afterCalls != calls {
		t.Fatal("GET executed native source calls")
	}
}
func m5InteractiveSorted(t *testing.T, v map[string]any) {
	t.Helper()
	rows := m5InteractiveRows(t, v, "results")
	if len(rows) < 2 {
		t.Fatal("ranking requires multiple candidates")
	}
	for i, raw := range rows {
		r := raw.(map[string]any)
		score, ok := r["score"].(float64)
		if !ok || score <= 0 {
			t.Fatalf("current complete quality profile was not ranked: %v", r)
		}
		if i == 0 {
			continue
		}
		prev := rows[i-1].(map[string]any)
		ps := prev["score"].(float64)
		seeds := r["seeders"].(float64)
		pseeds := prev["seeders"].(float64)
		if ps < score || (ps == score && pseeds < seeds) || (ps == score && pseeds == seeds && prev["id"].(string) > r["id"].(string)) {
			t.Fatalf("global quality/seeders/ID order violated at %d", i)
		}
	}
	if rows[0].(map[string]any)["seeders"] == float64(9999) {
		t.Fatal("seeder count outranked quality")
	}
}

func TestM5InteractiveSearchProgressiveNativeHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	slow := m5InteractiveNative(t, f, "slow-unknown", 2, true, nil)
	fast := m5InteractiveNative(t, f, "fast-offline", 3, false, slow.entered)
	disabled := m5InteractiveNative(t, f, "disabled", 1, false, nil)
	deleted := m5InteractiveNative(t, f, "deleted", 1, false, nil)
	// Unknown/unobserved health is represented by not_checked + NULL timestamps;
	// the persisted legacy health enum has no literal "unknown" value.
	m5InteractiveSQL(t, f, `UPDATE indexer SET health_checked_at=NULL,health_observed_at=NULL,health_reason='not_checked' WHERE id=$1`, uuid.MustParse(slow.id))
	m5InteractiveSQL(t, f, `UPDATE indexer SET health='offline' WHERE id=$1`, uuid.MustParse(fast.id))
	m5InteractiveSQL(t, f, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$1`, uuid.MustParse(disabled.id))
	m5IndexerStatus(t, f.request(t, "DELETE", "/api/v1/indexers/"+deleted.id+"?expected_version=1", "delete-before-search", nil, f.session), 204)
	if m5InteractiveInt(t, f, `SELECT count(*) FROM indexer WHERE id=$1 AND health_checked_at IS NULL AND health_observed_at IS NULL AND health_reason='not_checked'`, uuid.MustParse(slow.id)) != 1 {
		t.Fatal("slow source must start with unobserved health")
	}
	media := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO media(id,title,media_type) VALUES($1,'Interactive Movie','movie')`, media)
	created := m5InteractiveCreate(t, f, "progressive-create", map[string]any{"query": "Interactive Movie", "media_id": media.String()})
	h := m5IndexerObject(t, created)
	id := m5InteractiveString(t, h, "id")
	if h["source_count"] != float64(2) || h["media_id"] != media.String() {
		t.Fatal("enabled source set or native DB UUID scope wrong", h)
	}
	start, e := time.Parse(time.RFC3339Nano, h["created_at"].(string))
	if e != nil {
		t.Fatal(e)
	}
	deadline, e := time.Parse(time.RFC3339Nano, h["deadline"].(string))
	if e != nil || deadline.Sub(start) != 30*time.Second {
		t.Fatal("search window must be exactly 30 seconds")
	}
	m5InteractiveAwait(t, slow.entered, "slow source entry")
	progressive := m5InteractiveWaitSnapshot(t, f, id, func(v map[string]any) bool { return len(m5InteractiveRows(t, v, "results")) == 3 })
	if !slow.blocked.Load() || slow.cancelled.Load() {
		t.Fatal("fast candidates were not visible while slow native request remained blocked")
	}
	if !progressive["partial"].(bool) || progressive["statistics_mode"] != "latest_persisted" || progressive["sort_order"] != "quality_desc_seeders_desc_id_asc" || progressive["total_candidates"] != float64(3) {
		t.Fatal("progressive snapshot metadata wrong", progressive)
	}
	m5InteractiveSorted(t, progressive)
	progressiveScores := make(map[string]float64)
	for _, raw := range m5InteractiveRows(t, progressive, "results") {
		row := raw.(map[string]any)
		progressiveScores[row["id"].(string)] = row["score"].(float64)
	}
	slow.unblock()
	m5InteractiveAwait(t, slow.returned, "released slow response")
	complete := m5InteractiveWaitSnapshot(t, f, id, m5InteractiveTerminal)
	if len(m5InteractiveRows(t, complete, "results")) != 5 || complete["total_candidates"] != float64(5) || complete["truncated"] != false {
		t.Fatal("completed candidate set incomplete", complete)
	}
	m5InteractiveSorted(t, complete)
	for _, raw := range m5InteractiveRows(t, complete, "results") {
		row := raw.(map[string]any)
		if score, existed := progressiveScores[row["id"].(string)]; existed && row["score"] != score {
			t.Fatal("global quality score changed when slow source completed")
		}
	}
	for _, s := range []*m5InteractiveSource{slow, fast} {
		if m5InteractiveSourceView(t, complete, s.id)["state"] != "succeeded" || s.calls.Load() != 1 {
			t.Fatal("unknown/offline enabled source not queried exactly once")
		}
	}
	if disabled.calls.Load() != 0 || deleted.calls.Load() != 0 {
		t.Fatal("disabled/deleted source reached native HTTP")
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM indexer_query WHERE query_type='interactive'`) != 2 {
		t.Fatal("queries lack interactive purpose")
	}
	receiptPath := "/api/v1/release-search-commands/progressive-create?action=create"
	receipt := f.request(t, "GET", receiptPath, "", nil, f.session)
	m5IndexerStatus(t, receipt, 200)
	if !bytes.Equal(created.body, receipt.body) {
		t.Fatal("create receipt changed after completion")
	}
	m5InteractivePureReads(t, f, id, "progressive-create", slow, fast, disabled, deleted)
	// Public configuration change must fence the old source's earlier candidates.
	edited := f.request(t, "PUT", "/api/v1/indexers/"+fast.id, "change-fast-config", map[string]any{"name": "fast changed", "protocol": "torznab", "categories": []int{2000}, "expected_version": 1}, f.session)
	m5IndexerStatus(t, edited, 200)
	stale := m5InteractiveGet(t, f, id)
	if len(m5InteractiveRows(t, stale, "results")) != 2 || stale["partial"] != true {
		t.Fatal("stale source retained earlier candidates", stale)
	}
	source := m5InteractiveSourceView(t, stale, fast.id)
	if source["state"] != "stale" && source["reason"] != "source_changed" {
		t.Fatal("stale configuration lacks source status", source)
	}
	if old := f.request(t, "GET", receiptPath, "", nil, f.session); !bytes.Equal(old.body, created.body) {
		t.Fatal("original receipt recomputed after source configuration changed")
	}
	t.Log("M5_INTERACTIVE_PROGRESSIVE_NATIVE_PG_PASS: unknown/offline, enabled scope, fast-before-slow, current parser quality, global score/seeders/ID, pure GET, stale candidates")
}

func TestM5InteractiveSearchStrictValidationAndConcurrentReceiptHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	s := m5InteractiveNative(t, f, "validation", 0, true, nil)
	before := m5InteractiveReadFingerprint(t, f)
	cases := []struct{ name, body, key string }{
		{"missing-query", `{}`, "invalid-missing"}, {"null", `null`, "invalid-null"}, {"empty", `{"query":""}`, "invalid-empty"}, {"blank", `{"query":"   "}`, "invalid-blank"}, {"control", `{"query":"bad\nquery"}`, "invalid-control"},
		{"unknown-field", `{"query":"Movie","unexpected":true}`, "invalid-unknown"}, {"bad-media", `{"query":"Movie","media_id":"bad"}`, "invalid-media"}, {"bad-subscription", `{"query":"Movie","subscription_id":"bad"}`, "invalid-sub"}, {"bad-type", `{"query":42}`, "invalid-type"},
		{"extra-json", `{"query":"Movie"}{}`, "invalid-extra"}, {"missing-key", `{"query":"Movie"}`, ""}, {"short-key", `{"query":"Movie"}`, "tiny"}, {"bad-key", `{"query":"Movie"}`, "invalid key!"}, {"long-key", `{"query":"Movie"}`, strings.Repeat("x", 131)},
		{"long-query", `{"query":"` + strings.Repeat("x", 257) + `"}`, "invalid-long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m5IndexerStatus(t, f.request(t, "POST", m5InteractivePath, tc.key, tc.body, f.session), 400)
		})
	}
	m5IndexerStatus(t, f.request(t, "GET", m5InteractivePath+"/not-a-uuid", "", nil, f.session), 400)
	m5IndexerStatus(t, f.request(t, "POST", m5InteractivePath+"/not-a-uuid/cancel", "invalid-cancel", nil, f.session), 400)
	m5IndexerStatus(t, f.request(t, "GET", "/api/v1/releases/search?query=Movie", "", nil, f.session), 400)
	if s.calls.Load() != 0 || m5InteractiveReadFingerprint(t, f) != before {
		t.Fatal("invalid or legacy GET executed search/writes")
	}
	// Independent clients start behind one gate; failures are returned to the test
	// goroutine and every HTTP request is joined even on assertion failure.
	const n = 8
	var wg sync.WaitGroup
	var results [n]m5IndexerHTTPResult
	var errs [n]error
	gate := make(chan struct{})
	client := &http.Client{Timeout: 15 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			request, e := http.NewRequestWithContext(t.Context(), "POST", f.base+m5InteractivePath, strings.NewReader(`{"query":"Concurrent Movie"}`))
			if e != nil {
				errs[i] = e
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", f.base)
			request.Header.Set("Idempotency-Key", "concurrent-create")
			request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: f.session})
			response, e := client.Do(request)
			if e != nil {
				errs[i] = e
				return
			}
			defer func() { _ = response.Body.Close() }()
			body, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			errs[i] = e
			results[i] = m5IndexerHTTPResult{response.StatusCode, body, response.Header}
		}()
	}
	close(gate)
	wg.Wait()
	for i, r := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		m5IndexerStatus(t, r, 201)
		if !bytes.Equal(r.body, results[0].body) {
			t.Fatal("same-key concurrent create returned different stable headers")
		}
	}
	id := m5InteractiveString(t, m5IndexerObject(t, results[0]), "id")
	if m5InteractiveInt(t, f, `SELECT count(*) FROM interactive_search`) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM job WHERE kind='interactive_search_source'`) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM idempotency_record WHERE idempotency_key='interactive_search.create:concurrent-create'`) != 1 {
		t.Fatal("same-key create duplicated search/jobs/command rows")
	}
	m5IndexerStatus(t, f.request(t, "POST", m5InteractivePath, "concurrent-create", map[string]string{"query": "Changed"}, f.session), 409)
	m5IndexerStatus(t, f.request(t, "POST", m5InteractivePath+"/"+id+"/cancel", "payload-cancel", map[string]any{}, f.session), 400)
	m5IndexerStatus(t, f.request(t, "POST", m5InteractivePath+"/"+id+"/cancel", "", nil, f.session), 400)
	s.unblock()
	m5InteractiveWaitSnapshot(t, f, id, m5InteractiveTerminal)
	recovered := f.request(t, "GET", "/api/v1/release-search-commands/concurrent-create?action=create", "", nil, f.session)
	m5IndexerStatus(t, recovered, 200)
	if !bytes.Equal(recovered.body, results[0].body) {
		t.Fatal("recovered command changed after completion")
	}
	m5InteractivePureReads(t, f, id, "concurrent-create", s)
	t.Log("M5_INTERACTIVE_STRICT_CONCURRENT_PG_PASS: invalid/unknown JSON, UUID/key, legacy GET, eight real concurrent commands exactly once, stable original receipt")
}

func TestM5InteractiveSearchCancellationFenceHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	s := m5InteractiveNative(t, f, "cancel-held", 50, true, nil)
	created := m5InteractiveCreate(t, f, "cancel-create", map[string]string{"query": "Cancel Movie"})
	id := m5InteractiveString(t, m5IndexerObject(t, created), "id")
	m5InteractiveAwait(t, s.entered, "cancel source native entry")
	cancelled := f.request(t, "POST", m5InteractivePath+"/"+id+"/cancel", "cancel-command", nil, f.session)
	m5IndexerStatus(t, cancelled, 200)
	if m5IndexerObject(t, cancelled)["cancelled_at"] == nil {
		t.Fatal("cancel receipt lacks timestamp")
	}
	snapshot := m5InteractiveGet(t, f, id)
	if snapshot["state"] != "cancelled" || len(m5InteractiveRows(t, snapshot, "results")) != 0 {
		t.Fatal("cancel not visible in pure snapshot", snapshot)
	}
	if s.calls.Load() != 1 {
		t.Fatal("cancel or GET initiated a new native request")
	}
	s.unblock()
	m5InteractiveAwait(t, s.returned, "cancelled source HTTP return")
	m5InteractiveQuiesce(t, f)
	after := m5InteractiveGet(t, f, id)
	if after["state"] != "cancelled" || len(m5InteractiveRows(t, after, "results")) != 0 || m5InteractiveInt(t, f, `SELECT count(*) FROM release_candidate`) != 0 {
		t.Fatal("late source response published candidates after cancellation")
	}
	for _, tc := range []struct {
		key, action string
		want        m5IndexerHTTPResult
	}{{"cancel-create", "create", created}, {"cancel-command", "cancel", cancelled}} {
		r := f.request(t, "GET", "/api/v1/release-search-commands/"+tc.key+"?action="+tc.action, "", nil, f.session)
		m5IndexerStatus(t, r, 200)
		if !bytes.Equal(r.body, tc.want.body) {
			t.Fatal("original create/cancel receipt changed")
		}
	}
	replay := f.request(t, "POST", m5InteractivePath+"/"+id+"/cancel", "cancel-command", nil, f.session)
	m5IndexerStatus(t, replay, 200)
	if !bytes.Equal(replay.body, cancelled.body) {
		t.Fatal("cancel replay changed timestamp/header")
	}
	m5InteractivePureReads(t, f, id, "cancel-create", s)
	t.Log("M5_INTERACTIVE_CANCEL_FENCE_PG_PASS: explicit native entry, durable cancel, late candidate fence, stable create/cancel receipts and pure terminal GET")
}

func m5InteractiveLogin(t *testing.T, f *m5IndexerAcceptance, name string) string {
	t.Helper()
	r := f.request(t, "POST", "/api/v1/auth/login", "", map[string]string{"username": name, "password": "OwnedM5NativeFixture2026!"}, "")
	m5IndexerStatus(t, r, 200)
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == "pulsemedia_session" {
			return c.Value
		}
	}
	t.Fatal("login did not issue Cookie")
	return ""
}
func m5InteractiveDenied(t *testing.T, r m5IndexerHTTPResult, statuses ...int) {
	t.Helper()
	for _, status := range statuses {
		if r.status == status {
			var v map[string]any
			if json.Unmarshal(r.body, &v) != nil || v["id"] != nil || v["search"] != nil || v["results"] != nil || v["query"] != nil {
				t.Fatal("denied response exposed privileged search data")
			}
			return
		}
	}
	t.Fatalf("access status %d, expected %v", r.status, statuses)
}

func TestM5InteractiveSearchCurrentOwnerSessionScopeHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	s := m5InteractiveNative(t, f, "auth", 0, false, nil)
	member, other := uuid.New(), uuid.New()
	for _, u := range []struct {
		id         uuid.UUID
		name, role string
	}{{member, "interactive-member", "member"}, {other, "interactive-other-admin", "admin"}} {
		m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,$2,password_hash,$3,true FROM app_user WHERE username='m5-native-admin'`, u.id, u.name, u.role)
	}
	memberSession := m5InteractiveLogin(t, f, "interactive-member")
	otherSession := m5InteractiveLogin(t, f, "interactive-other-admin")
	media, root, sub := uuid.New(), uuid.New(), uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO media(id,title,media_type) VALUES($1,'Scoped Movie','movie')`, media)
	m5InteractiveSQL(t, f, `INSERT INTO library_root(id,path,media_type,name) VALUES($1,'/configured-interactive-test-root','movie','interactive root')`, root)
	m5InteractiveSQL(t, f, `INSERT INTO subscription(id,user_id,media_id,root_id,state) VALUES($1,$2,$3,$4,'paused')`, sub, other, media, root)
	scopeBody := map[string]any{"query": "Scoped Movie", "media_id": media.String(), "subscription_id": sub.String()}
	m5InteractiveDenied(t, f.request(t, "POST", m5InteractivePath, "foreign-subscription", scopeBody, memberSession), 403, 404)
	m5InteractiveSQL(t, f, `UPDATE subscription SET user_id=$2 WHERE id=$1`, sub, member)
	created := f.request(t, "POST", m5InteractivePath, "member-owned-create", scopeBody, memberSession)
	m5IndexerStatus(t, created, 201)
	id := m5InteractiveString(t, m5IndexerObject(t, created), "id")
	paths := []string{m5InteractivePath + "/" + id, "/api/v1/release-search-commands/member-owned-create?action=create"}
	for _, path := range paths {
		m5InteractiveDenied(t, f.request(t, "GET", path, "", nil, otherSession), 403, 404)
		m5InteractiveDenied(t, f.request(t, "GET", path, "", nil, ""), 401)
	}
	m5InteractiveDenied(t, f.request(t, "POST", m5InteractivePath+"/"+id+"/cancel", "other-cancel", nil, otherSession), 403, 404)
	// A public read-scoped token remains a token even though its owner is admin.
	token := f.request(t, "POST", "/api/v1/auth/tokens", "interactive-read-token", map[string]string{"name": "interactive readonly"}, f.session)
	m5IndexerStatus(t, token, 200)
	secret := m5InteractiveString(t, m5IndexerObject(t, token), "secret")
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	for _, path := range append(paths, m5InteractivePath) {
		method := "GET"
		var body io.Reader
		if path == m5InteractivePath {
			method = "POST"
			body = strings.NewReader(`{"query":"Token Movie"}`)
		}
		r, e := http.NewRequestWithContext(t.Context(), method, f.base+path, body)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "token-create-key")
		response, e := client.Do(r)
		if e != nil {
			t.Fatal("token request failed")
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		m5InteractiveDenied(t, m5IndexerHTTPResult{response.StatusCode, raw, response.Header}, 403)
	}
	// Current object owner and current role are rechecked for both snapshot and receipt.
	m5InteractiveSQL(t, f, `UPDATE subscription SET user_id=$2 WHERE id=$1`, sub, other)
	for _, path := range paths {
		m5InteractiveDenied(t, f.request(t, "GET", path, "", nil, memberSession), 403, 404)
	}
	m5InteractiveSQL(t, f, `UPDATE subscription SET user_id=$2 WHERE id=$1`, sub, member)
	// Use the administrator's own search to verify an admin->member demotion.
	adminCreated := m5InteractiveCreate(t, f, "admin-owned-create", map[string]string{"query": "Admin Movie"})
	adminID := m5InteractiveString(t, m5IndexerObject(t, adminCreated), "id")
	adminPaths := []string{m5InteractivePath + "/" + adminID, "/api/v1/release-search-commands/admin-owned-create?action=create"}
	m5InteractiveSQL(t, f, `UPDATE app_user SET role='member' WHERE username='m5-native-admin'`)
	for _, path := range adminPaths {
		m5InteractiveDenied(t, f.request(t, "GET", path, "", nil, f.session), 401, 403)
	}
	m5InteractiveSQL(t, f, `UPDATE app_user SET role='admin',is_active=false WHERE username='m5-native-admin'`)
	for _, path := range adminPaths {
		m5InteractiveDenied(t, f.request(t, "GET", path, "", nil, f.session), 401, 403)
	}
	m5InteractiveSQL(t, f, `UPDATE app_user SET is_active=true WHERE username='m5-native-admin'`)
	m5InteractiveSQL(t, f, `DELETE FROM app_session WHERE user_id=(SELECT id FROM app_user WHERE username='m5-native-admin')`)
	for _, path := range adminPaths {
		m5InteractiveDenied(t, f.request(t, "GET", path, "", nil, f.session), 401)
	}
	// This fixture's App joins any already-running work after the last auth checks.
	_ = s
	t.Log("M5_INTERACTIVE_CURRENT_OWNER_PG_PASS: member/admin Cookie, foreign owner/admin, foreign subscription, read token, changed object owner, demoted/inactive/deleted session")
}

func TestM5InteractiveSearchNativePaginationBoundHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	s := m5InteractiveNative(t, f, "paged", 250, false, nil)
	created := m5InteractiveCreate(t, f, "paged-create", map[string]string{"query": "Paged Movie"})
	id := m5InteractiveString(t, m5IndexerObject(t, created), "id")
	snapshot := m5InteractiveWaitSnapshot(t, f, id, m5InteractiveTerminal)
	m5InteractiveQuiesce(t, f)
	s.mu.Lock()
	offsets := append([]int(nil), s.offsets...)
	s.mu.Unlock()
	if fmt.Sprint(offsets) != "[0 50 100 150]" || s.calls.Load() != 4 {
		t.Fatalf("native pagination must stop after four verified pages: %v", offsets)
	}
	if len(m5InteractiveRows(t, snapshot, "results")) != 200 || snapshot["total_candidates"] != float64(200) || snapshot["partial"] != true {
		t.Fatal("bounded pagination must retain partial retrieved candidates", snapshot)
	}
	if len(m5InteractiveRows(t, m5InteractiveSourceView(t, snapshot, s.id), "query_ids")) != 4 {
		t.Fatal("snapshot lost persisted page links")
	}
	m5InteractiveSorted(t, snapshot)
	m5InteractivePureReads(t, f, id, "paged-create", s)
	t.Log("M5_INTERACTIVE_PAGINATION_PG_PASS: native total/offset proof, offsets 0/50/100/150, four-page bound, 200 durable candidates, partial and pure GET")
}

func TestM5InteractiveSearchPersistentBudgetsAndSourceCapHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	next := m5InteractiveNative(t, f, "next-query-budget", 0, false, nil)
	limited := m5InteractiveNative(t, f, "rate-limit-budget", 0, false, nil)
	m5InteractiveSQL(t, f, `UPDATE indexer SET next_query_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, uuid.MustParse(next.id))
	m5InteractiveSQL(t, f, `UPDATE indexer SET rate_limit_until=clock_timestamp()+interval '1 hour' WHERE id=$1`, uuid.MustParse(limited.id))
	created := m5InteractiveCreate(t, f, "budget-create", map[string]string{"query": "Budget Movie"})
	id := m5InteractiveString(t, m5IndexerObject(t, created), "id")
	snapshot := m5InteractiveWaitSnapshot(t, f, id, m5InteractiveTerminal)
	if next.calls.Load() != 0 || limited.calls.Load() != 0 || len(m5InteractiveRows(t, snapshot, "results")) != 0 || snapshot["partial"] != true {
		t.Fatal("persistent endpoint budget/rate limit bypassed")
	}
	for _, s := range []*m5InteractiveSource{next, limited} {
		state := m5InteractiveSourceView(t, snapshot, s.id)["state"]
		if state != "deferred" {
			t.Fatalf("budgeted source state %v, want deferred", state)
		}
	}
	// Public configuration creates every source; admission must count the entire
	// enabled/nondeleted set rather than truncating it or filtering by health.
	for i := 2; i < 64; i++ {
		r := f.request(t, "POST", "/api/v1/indexers", fmt.Sprintf("cap-source-%03d", i), map[string]any{"name": fmt.Sprintf("cap-%03d", i), "protocol": "torznab", "url": f.rawURL, "api_key": "m5-fixture=credential", "categories": []int{2000}}, f.session)
		m5IndexerStatus(t, r, 201)
	}
	m5InteractiveSQL(t, f, `UPDATE indexer SET next_query_at=clock_timestamp()+interval '1 hour'`)
	admitted := m5InteractiveCreate(t, f, "source-cap-64", map[string]string{"query": "All Sources"})
	admittedHeader := m5IndexerObject(t, admitted)
	if admittedHeader["source_count"] != float64(64) {
		t.Fatal("64 enabled sources were silently truncated", admittedHeader)
	}
	admittedID := m5InteractiveString(t, admittedHeader, "id")
	m5InteractiveWaitSnapshot(t, f, admittedID, m5InteractiveTerminal)
	m5InteractiveQuiesce(t, f)
	m5IndexerStatus(t, f.request(t, "POST", "/api/v1/indexers", "source-number-65", map[string]any{"name": "cap-65", "protocol": "torznab", "url": f.rawURL, "api_key": "m5-fixture=credential", "categories": []int{2000}}, f.session), 201)
	before := m5InteractiveReadFingerprint(t, f)
	m5IndexerStatus(t, f.request(t, "POST", m5InteractivePath, "source-cap-65", map[string]string{"query": "Too Many Sources"}, f.session), 400)
	if after := m5InteractiveReadFingerprint(t, f); after != before {
		t.Fatal("65-source rejection wrote partial search/command/query rows")
	}
	if next.calls.Load() != 0 || limited.calls.Load() != 0 || f.calls.Load() != 0 {
		t.Fatal("budgeted source cap acceptance reached network")
	}
	t.Log("M5_INTERACTIVE_BUDGET_SOURCE_CAP_PG_PASS: persistent next_query_at and rate_limit_until, deferred sources, exactly 64 admitted, 65 rejected atomically")
}

func TestM5InteractiveSearchNativePageTimeoutDoesNotBlockFastHTTPPG(t *testing.T) {
	f := newM5IndexerAcceptance(t)
	slow := m5InteractiveNative(t, f, "page-timeout", 1, true, nil)
	fast := m5InteractiveNative(t, f, "timeout-fast", 2, false, slow.entered)
	created := m5InteractiveCreate(t, f, "page-timeout-create", map[string]string{"query": "Timeout Movie"})
	id := m5InteractiveString(t, m5IndexerObject(t, created), "id")
	m5InteractiveAwait(t, slow.entered, "timed source entry")
	progressive := m5InteractiveWaitSnapshot(t, f, id, func(v map[string]any) bool { return len(m5InteractiveRows(t, v, "results")) == 2 })
	if !slow.blocked.Load() || slow.cancelled.Load() {
		t.Fatal("fast page was not visible before native slow timeout")
	}
	m5InteractiveSorted(t, progressive)
	// Do not release the source: the real native client's page deadline must
	// cancel its request, and the server observes that cancellation directly.
	m5InteractiveAwait(t, slow.returned, "native page deadline cancellation")
	if !slow.cancelled.Load() {
		t.Fatal("native page returned without cancellation or test release")
	}
	snapshot := m5InteractiveWaitSnapshot(t, f, id, m5InteractiveTerminal)
	if m5InteractiveSourceView(t, snapshot, slow.id)["state"] != "timeout" || m5InteractiveSourceView(t, snapshot, fast.id)["state"] != "succeeded" || snapshot["partial"] != true || len(m5InteractiveRows(t, snapshot, "results")) != 2 {
		t.Fatal("single source timeout lost fast results or timeout status", snapshot)
	}
	m5InteractivePureReads(t, f, id, "page-timeout-create", slow, fast)
	t.Log("M5_INTERACTIVE_NATIVE_PAGE_TIMEOUT_PG_PASS: native client cancellation observed, fast result before slow timeout, partial result retained, no detached calls")
}
