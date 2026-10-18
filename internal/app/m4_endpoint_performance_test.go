package app

// Independent opt-in M4-04 active endpoint evidence. This deliberately omits the
// separate 1000-subscription/100000-file baseline. Only synthetic loopback I/O.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const m4EndpointRounds = 20
const m4EndpointLatency = 100 * time.Millisecond

type m4EndpointObserver struct {
	mu                     sync.Mutex
	starts                 []time.Time
	active, maximum, calls int
}

func (o *m4EndpointObserver) enter() func() {
	o.mu.Lock()
	o.calls++
	o.active++
	if o.active > o.maximum {
		o.maximum = o.active
	}
	o.starts = append(o.starts, time.Now())
	o.mu.Unlock()
	return func() { o.mu.Lock(); o.active--; o.mu.Unlock() }
}

func m4EndpointMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestM4EndpointPerformance(t *testing.T) {
	// Skip before creating any fixture or opening any database/network resource.
	if os.Getenv("PULSE_M4_ENDPOINT_PERFORMANCE") != "1" {
		t.Skip("opt in with mise exec -- bash scripts/m4_endpoint_performance.sh")
	}
	scratch := os.Getenv("TMPDIR")
	if !strings.HasPrefix(scratch, "/data/temp/pulse-podman-test-pg.") || filepath.Base(scratch) != "build" || os.Getenv("TMP") != scratch || os.Getenv("TEMP") != scratch {
		t.Fatal("requires direct private Podman PG wrapper with identical temporary roots")
	}
	pgURL, err := url.Parse(os.Getenv("PULSE_TEST_DATABASE_URL"))
	m4EndpointMust(t, err)
	if pgURL.Scheme != "postgres" || pgURL.Query().Get("host") != filepath.Join(filepath.Dir(scratch), "socket") {
		t.Fatal("requires explicit owned PG unix socket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, dsn := bootstrapTestPool(t)
	for _, setting := range []string{"fsync", "synchronous_commit", "full_page_writes"} {
		var value string
		m4EndpointMust(t, pool.QueryRow(ctx, "SELECT current_setting($1)", setting).Scan(&value))
		if value != "on" {
			t.Fatalf("durability %s=%s", setting, value)
		}
		t.Logf("M4_ENDPOINT_DURABILITY %s=%s", setting, value)
	}
	var pgVersion string
	m4EndpointMust(t, pool.QueryRow(ctx, "SHOW server_version").Scan(&pgVersion))
	t.Logf("M4_ENDPOINT_ENV Go=%s GOOS=%s GOARCH=%s logical_CPUs=%d GOMAXPROCS=%d PostgreSQL=%s binary=normal_optimized_go_test App=in_process fixtures=in_process", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), pgVersion)
	data := t.TempDir()
	key, err := crypto.EnsureMasterKey(data, "")
	m4EndpointMust(t, err)
	q := db.New(pool)
	user, err := q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "m4-endpoint-admin", PasswordHash: "unused-session-only", Role: ports.RoleAdmin, IsActive: true})
	m4EndpointMust(t, err)
	token, tokenHash, err := crypto.GenerateToken("pm_api_")
	m4EndpointMust(t, err)
	_, err = q.CreateAPIToken(ctx, db.CreateAPITokenParams{ID: uuid.New(), UserID: user.ID, Name: "synthetic read only", TokenHash: tokenHash, Scopes: []byte(`["read"]`), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	m4EndpointMust(t, err)
	setup := search.NewService(postgres.NewSearchExecutionStore(pool), idgen.New(), crypto.NewSecretProtector(key)) // Production default 2s.
	// A second real Service verifies the cross-instance physical permit while the
	// App's first endpoint request is still physically executing.
	competitor := search.NewService(postgres.NewSearchExecutionStore(pool), idgen.New(), crypto.NewSecretProtector(key))
	competitor.SetClientFactory(newIndexerClient)
	competitor.SetEndpointRepository(postgres.NewSearchEndpointStore(pool))
	var permitRejected atomic.Int64
	observers := make([]*m4EndpointObserver, 10)
	ids := make([]uuid.UUID, 10)
	secrets := make([]string, 10)
	for n := range 10 {
		observers[n] = &m4EndpointObserver{}
		secrets[n] = fmt.Sprintf("m4-local-synthetic-key-%02d", n)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			leave := observers[n].enter()
			defer leave()
			if r.URL.Path != "/api" || r.URL.Query().Get("apikey") != secrets[n] || r.URL.Query().Get("t") != "movie" || r.URL.Query().Get("offset") != "" || r.URL.Query().Get("limit") != "50" {
				t.Error("unexpected synthetic endpoint request shape or credential")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			keyword := r.URL.Query().Get("q")
			if n == 0 && keyword == "m4-config-fence" {
				_, e := competitor.ExecuteSearch(r.Context(), ids[n], "m4-competing-physical-call", 0)
				if !errors.Is(e, ports.ErrUnavailable) {
					t.Errorf("competing physical request not rejected: %v", e)
				} else {
					permitRejected.Add(1)
				}
				if _, e = pool.Exec(r.Context(), "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", ids[n]); e != nil {
					t.Error(e)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
			}
			timer := time.NewTimer(m4EndpointLatency)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return
			}
			total := 1
			if n == 0 && keyword == "m4-incomplete" {
				total = 2
			}
			digest := sha256.Sum256([]byte(keyword))
			w.Header().Set("Content-Type", "application/xml")
			_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="%d"/><item><title>Synthetic.Film.2020.1080p.BluRay.x264-GROUP%d</title><guid>m4-%02d-%x</guid><enclosure url="http://%s/local-synthetic.torrent?passkey=fixture-only" length="1024"/><torznab:attr name="seeders" value="10"/></item></channel></rss>`, total, n, n, digest, r.Host)
		}))
		t.Cleanup(server.Close)
		idx, e := setup.CreateIndexer(ctx, fmt.Sprintf("M4 synthetic %02d", n), server.URL, secrets[n], nil)
		m4EndpointMust(t, e)
		ids[n] = idx.ID
		if bytes.Contains(idx.ApiKeyEncrypted, []byte(secrets[n])) {
			t.Fatal("plaintext endpoint credential")
		}
		plain, e := crypto.Decrypt(key, idx.ApiKeyEncrypted)
		m4EndpointMust(t, e)
		if string(plain) != secrets[n] {
			t.Fatal("endpoint credential does not decrypt with App key")
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	m4EndpointMust(t, err)
	addr := listener.Addr().String()
	m4EndpointMust(t, listener.Close())
	cfg := config.NewDefault()
	cfg.DataDir, cfg.Database.URL, cfg.Server.HTTPAddr = data, dsn, addr
	app, err := New(cfg, WithLogger(logging.NewLogger(io.Discard, "error")))
	m4EndpointMust(t, err)
	runCtx, stopRun := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx) }()
	// Registered after endpoint servers: App/HTTP/workers join before servers and DB.
	stop := sync.OnceFunc(func() {
		stopRun()
		select {
		case e := <-done:
			if e != nil {
				t.Errorf("App.Run: %v", e)
			}
		case <-time.After(50 * time.Second):
			t.Error("App.Run drain exceeded budget")
		}
		if e := app.Close(); e != nil {
			t.Error(e)
		}
		t.Log("M4_ENDPOINT_APP_HTTP_AND_WORKERS_JOINED")
	})
	t.Cleanup(stop)
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 16, MaxIdleConnsPerHost: 4}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	base := "http://" + addr
	ready := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		resp, e := client.Get(base + "/readyz")
		if e == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ready = resp.StatusCode == 200
		}
		if ready {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatal("App readiness timeout")
	}
	request := func(keyword, credential string, status int) ([]byte, time.Duration) {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/releases/search?query="+url.QueryEscape(keyword), nil)
		m4EndpointMust(t, e)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		started := time.Now()
		resp, e := client.Do(req)
		m4EndpointMust(t, e)
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		elapsed := time.Since(started)
		m4EndpointMust(t, e)
		if resp.StatusCode != status {
			t.Fatalf("%s HTTP=%d want=%d", keyword, resp.StatusCode, status)
		}
		for _, secret := range append(append([]string{}, secrets...), token, "fixture-only", "link_encrypted", "api_key_encrypted") {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("sensitive data in public search response")
			}
		}
		return raw, elapsed
	}
	checkResult := func(raw []byte, count int, partial bool) []uuid.UUID {
		t.Helper()
		var response struct {
			Results []struct {
				ID          uuid.UUID `json:"id"`
				IndexerName string    `json:"indexer_name"`
			} `json:"results"`
			Partial bool `json:"partial"`
		}
		m4EndpointMust(t, json.Unmarshal(raw, &response))
		if len(response.Results) != count || response.Partial != partial {
			t.Fatalf("results=%d partial=%t want=%d/%t", len(response.Results), response.Partial, count, partial)
		}
		seen := map[string]bool{}
		resultIDs := make([]uuid.UUID, 0, count)
		for _, row := range response.Results {
			if seen[row.IndexerName] || !strings.HasPrefix(row.IndexerName, "M4 synthetic ") {
				t.Fatal("duplicate or unexpected endpoint result")
			}
			seen[row.IndexerName] = true
			resultIDs = append(resultIDs, row.ID)
		}
		return resultIDs
	}
	countCalls := func() int {
		total := 0
		for _, o := range observers {
			o.mu.Lock()
			total += o.calls
			o.mu.Unlock()
		}
		return total
	}
	request("m4-unauthenticated", "", http.StatusUnauthorized)
	_, err = pool.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, user.ID)
	m4EndpointMust(t, err)
	request("m4-inactive-current-account", token, http.StatusUnauthorized)
	_, err = pool.Exec(ctx, `UPDATE app_user SET is_active=true WHERE id=$1`, user.ID)
	m4EndpointMust(t, err)
	if countCalls() != 0 {
		t.Fatal("unauthorized search reached endpoint")
	}

	// Offer only after every persisted budget is due, with a 100ms margin.
	// This reads the production budget; it never changes or resets it. Endpoint
	// work is bounded to four workers per aggregate call.
	pace := func() {
		var seconds float64
		m4EndpointMust(t, pool.QueryRow(ctx, `SELECT GREATEST(0,EXTRACT(EPOCH FROM (max(next_query_at)-clock_timestamp())))::double precision FROM indexer`).Scan(&seconds))
		timer := time.NewTimer(time.Duration(seconds*float64(time.Second)) + 100*time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	pace()
	raw, warm := request("m4-warmup", token, http.StatusOK)
	checkResult(raw, 10, false)
	t.Logf("M4_ENDPOINT_WARMUP samples=1 milliseconds=%.3f excluded_from_percentiles=true", float64(warm)/float64(time.Millisecond))
	// An immediate second offer must respect the unchanged persisted 2s budget.
	request("m4-immediate-rate-limit", token, http.StatusServiceUnavailable)
	if countCalls() != 10 {
		t.Fatal("immediate offer bypassed endpoint budget")
	}
	durations := make([]time.Duration, 0, m4EndpointRounds)
	var measuredWallStart time.Time
	for round := range m4EndpointRounds {
		pace()
		if round == 0 {
			measuredWallStart = time.Now()
		}
		keyword := fmt.Sprintf("m4-measured-%03d", round)
		raw, elapsed := request(keyword, token, http.StatusOK)
		resultIDs := checkResult(raw, 10, false)
		var queries, pages, links, current int
		m4EndpointMust(t, pool.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM indexer_query WHERE keyword=$1 AND state='succeeded' AND results_count=1),
		 (SELECT count(*) FROM indexer_query_page p JOIN indexer_query q ON q.id=p.query_id WHERE q.keyword=$1 AND p.offset_verified AND p.total=1 AND p.item_count=1 AND p.page_offset=0 AND p.page_limit=50),
		 (SELECT count(*) FROM indexer_query_candidate qc JOIN indexer_query q ON q.id=qc.query_id WHERE q.keyword=$1),
		 (SELECT count(*) FROM release_candidate c JOIN indexer i ON i.id=c.indexer_id WHERE c.id=ANY($2::uuid[]) AND c.source_config_version=i.config_version AND i.health='online')`, keyword, resultIDs).Scan(&queries, &pages, &links, &current))
		if queries != 10 || pages != 10 || links != 10 || current != 10 || countCalls() != (round+2)*10 {
			t.Fatalf("round=%d queries/pages/links/current/calls=%d/%d/%d/%d/%d", round, queries, pages, links, current, countCalls())
		}
		durations = append(durations, elapsed)
	}
	wall := time.Since(measuredWallStart)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	percentile := func(p float64) float64 {
		return float64(durations[int(math.Ceil(p*float64(len(durations))))-1]) / float64(time.Millisecond)
	}
	var measuredQueries, measuredCandidates, measuredPages, measuredLinks int
	m4EndpointMust(t, pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM indexer_query WHERE keyword LIKE 'm4-measured-%'),
	 (SELECT count(*) FROM release_candidate c JOIN indexer_query_candidate qc ON qc.candidate_id=c.id JOIN indexer_query q ON q.id=qc.query_id WHERE q.keyword LIKE 'm4-measured-%'),
	 (SELECT count(*) FROM indexer_query_page p JOIN indexer_query q ON q.id=p.query_id WHERE q.keyword LIKE 'm4-measured-%'),
	 (SELECT count(*) FROM indexer_query_candidate qc JOIN indexer_query q ON q.id=qc.query_id WHERE q.keyword LIKE 'm4-measured-%')`).Scan(&measuredQueries, &measuredCandidates, &measuredPages, &measuredLinks))
	if measuredQueries != 200 || measuredCandidates != 200 || measuredPages != 200 || measuredLinks != 200 {
		t.Fatal("measured persistent count mismatch")
	}
	t.Logf("M4_ENDPOINT_MEASURE route=/api/v1/releases/search rounds=%d samples=%d endpoint_HTTP_samples=%d concurrency=1 p50_ms=%.3f p95_ms=%.3f p99_ms=%.3f wall_seconds=%.3f rounds_per_second=%.3f endpoint_calls_per_second=%.3f", m4EndpointRounds, len(durations), measuredQueries, percentile(.50), percentile(.95), percentile(.99), wall.Seconds(), float64(m4EndpointRounds)/wall.Seconds(), float64(measuredQueries)/wall.Seconds())
	t.Logf("M4_ENDPOINT_PERSIST measured_queries=%d measured_candidates=%d measured_pages=%d measured_links=%d warmup_queries=10 actual_calls_before_negatives=%d", measuredQueries, measuredCandidates, measuredPages, measuredLinks, countCalls())

	pace()
	raw, _ = request("m4-incomplete", token, http.StatusOK)
	checkResult(raw, 10, true)
	var incomplete int
	m4EndpointMust(t, pool.QueryRow(ctx, `SELECT count(*) FROM indexer_query_page p JOIN indexer_query q ON q.id=p.query_id WHERE q.keyword='m4-incomplete' AND p.total=2 AND p.item_count=1`).Scan(&incomplete))
	if incomplete != 1 {
		t.Fatal("incomplete page lost its evidence")
	}
	pace()
	raw, _ = request("m4-config-fence", token, http.StatusOK)
	checkResult(raw, 9, true)
	var failed, staleCandidates int
	m4EndpointMust(t, pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM indexer_query WHERE keyword='m4-config-fence' AND state='failed'),
	 (SELECT count(*) FROM indexer_query_candidate qc JOIN indexer_query q ON q.id=qc.query_id WHERE q.keyword='m4-config-fence' AND q.indexer_id=$1)`, ids[0]).Scan(&failed, &staleCandidates))
	if failed != 1 || staleCandidates != 0 || permitRejected.Load() != 1 {
		t.Fatal("config or physical permit fence failed", failed, staleCandidates, permitRejected.Load())
	}
	pace()
	raw, _ = request("m4-config-recovered", token, http.StatusOK)
	checkResult(raw, 10, false)
	var recovered int
	m4EndpointMust(t, pool.QueryRow(ctx, `SELECT count(*) FROM indexer_query_candidate qc JOIN indexer_query q ON q.id=qc.query_id JOIN release_candidate c ON c.id=qc.candidate_id WHERE q.keyword='m4-config-recovered' AND q.indexer_id=$1 AND q.config_version=2 AND c.source_config_version=2`, ids[0]).Scan(&recovered))
	if recovered != 1 {
		t.Fatal("new configuration did not recover")
	}
	stop()
	var queries, succeeded, failedTotal, candidates, pages, links, subscriptions, files, unfinished int
	m4EndpointMust(t, pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM indexer_query),(SELECT count(*) FROM indexer_query WHERE state='succeeded'),(SELECT count(*) FROM indexer_query WHERE state='failed'),
	 (SELECT count(*) FROM release_candidate),(SELECT count(*) FROM indexer_query_page),(SELECT count(*) FROM indexer_query_candidate),
	 (SELECT count(*) FROM subscription),(SELECT count(*) FROM library_file),(SELECT count(*) FROM indexer_query WHERE state IN ('queued','running'))`).Scan(&queries, &succeeded, &failedTotal, &candidates, &pages, &links, &subscriptions, &files, &unfinished))
	if queries != 240 || succeeded != 239 || failedTotal != 1 || candidates != 239 || pages != 239 || links != 239 || subscriptions != 0 || files != 0 || unfinished != 0 || countCalls() != 240 {
		t.Fatalf("final counts queries=%d succeeded=%d failed=%d candidates=%d pages=%d links=%d subscriptions=%d files=%d unfinished=%d calls=%d", queries, succeeded, failedTotal, candidates, pages, links, subscriptions, files, unfinished, countCalls())
	}
	for n, o := range observers {
		o.mu.Lock()
		minimumGap := time.Hour
		for i := 1; i < len(o.starts); i++ {
			gap := o.starts[i].Sub(o.starts[i-1])
			if gap < minimumGap {
				minimumGap = gap
			}
		}
		calls, maximum, active := o.calls, o.maximum, o.active
		o.mu.Unlock()
		if calls != 24 || maximum != 1 || active != 0 || minimumGap < 2*time.Second {
			t.Fatalf("endpoint=%d calls=%d max=%d active=%d minimum_gap=%s", n, calls, maximum, active, minimumGap)
		}
		t.Logf("M4_ENDPOINT_OBSERVER endpoint=%02d calls=%d physical_max=%d final_inflight=%d min_start_gap_ms=%.3f", n, calls, maximum, active, float64(minimumGap)/float64(time.Millisecond))
	}
	rows, err := pool.Query(ctx, `SELECT link_encrypted FROM release_candidate`)
	m4EndpointMust(t, err)
	defer rows.Close()
	for rows.Next() {
		var encrypted []byte
		m4EndpointMust(t, rows.Scan(&encrypted))
		if bytes.Contains(encrypted, []byte("fixture-only")) {
			t.Fatal("plaintext candidate link")
		}
		plain, e := crypto.Decrypt(key, encrypted)
		m4EndpointMust(t, e)
		if !strings.Contains(string(plain), "passkey=fixture-only") {
			t.Fatal("candidate link decryption mismatch")
		}
	}
	m4EndpointMust(t, rows.Err())
	t.Logf("M4_ENDPOINT_FINAL calls=%d queries=%d succeeded=%d failed=%d candidates=%d pages=%d links=%d unfinished=%d encrypted_configurations=10 encrypted_candidate_links=%d", countCalls(), queries, succeeded, failedTotal, candidates, pages, links, unfinished, candidates)
	t.Log("M4_ENDPOINT_NEGATIVES unauthenticated=401 inactive_current_account=401 unauthorized_endpoint_calls=0 immediate_budget=503 budget_bypass_calls=0 concurrent_physical_permit=rejected stale_config_result=excluded persisted_failed_query=1 fresh_config_version=2 recovered=true incomplete_total=2/item_count=1 partial=true response_secret_leaks=0")
	t.Log("M4_ENDPOINT_SCOPE synthetic_loopback_endpoints=10 fixture_latency_ms=100 production_minimum_interval_seconds=2 offer_after_latest_persisted_budget_plus_ms=100 endpoint_physical_limit=1 HTTP_round_concurrency=1 sequential_endpoint_execution=false endpoint_worker_limit_per_aggregate=4 samples_nearest_rank=true warmup_rounds=1 measured_rounds=20 negative_active_rounds=3 PG_caches=warm durability=unchanged subscriptions=0 library_files=0 omits_1000_subscriptions_and_100000_files=true live_services=false payload_fetches=0 memory_and_fairness=unmeasured search_200ms_goal=not_asserted M4_acceptance=not_claimed")
}
