package app

// Opt-in, synthetic M4-04 baseline. Run scripts/m4_performance.sh, which owns a
// private PG18 container. HTTP and RSS measurements target the optimized normal
// cmd/pulsemedia binary; fixture generation/scanning happens in this test process.
// No endpoint is evidence for M4-01. No threshold assertion turns a goal into fact.
import (
	"context"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const m4FileCount = 100000
const m4SubscriptionCount = 1000

// This observer delegates every mutation to the production repository. Reading
// the committed cursor verifies actual page sizes, including the final page.
type m4ScanObserver struct {
	ports.ScanRepository
	pool                 *pgxpool.Pool
	previous             uuid.UUID
	calls, maxRows, rows int
}

func (o *m4ScanObserver) ReconcileBatch(ctx context.Context, seal ports.ScanSeal) (ports.ScanFinalizationProgress, error) {
	p, err := o.ScanRepository.ReconcileBatch(ctx, seal)
	if err != nil {
		return p, err
	}
	var cursor pgtype.UUID
	if err = o.pool.QueryRow(ctx, `SELECT cursor_file_id FROM scan_finalization WHERE scan_run_id=$1`, seal.RunID).Scan(&cursor); err != nil {
		return p, err
	}
	n := 0
	if cursor.Valid {
		if err = o.pool.QueryRow(ctx, `SELECT count(*) FROM library_file WHERE root_id=$1 AND id>$2 AND id<=$3`, seal.RootID, o.previous, uuid.UUID(cursor.Bytes)).Scan(&n); err != nil {
			return p, err
		}
		o.previous = uuid.UUID(cursor.Bytes)
	}
	o.calls++
	o.rows += n
	if n > o.maxRows {
		o.maxRows = n
	}
	if n > 128 {
		return p, fmt.Errorf("reconciliation exceeded 128 rows: %d", n)
	}
	return p, nil
}

func m4ID(kind, n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("%08x-0000-4000-8000-%012d", kind, n+1))
}
func m4Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func m4Int(t *testing.T, name string, low, high int) int {
	t.Helper()
	n, e := strconv.Atoi(os.Getenv(name))
	if e != nil || n < low || n > high {
		t.Fatalf("%s must be %d..%d; use scripts/m4_performance.sh", name, low, high)
	}
	return n
}

func TestM4PerformanceBaseline(t *testing.T) {
	if os.Getenv("PULSE_M4_PERFORMANCE") != "1" {
		t.Skip("opt in with mise exec -- bash scripts/m4_performance.sh")
	}
	scratch := os.Getenv("TMPDIR")
	if !strings.HasPrefix(scratch, "/data/temp/pulse-podman-test-pg.") {
		t.Fatal("requires private Podman PG wrapper scratch")
	}
	for _, k := range []string{"TMP", "TEMP"} {
		if os.Getenv(k) != scratch {
			t.Fatal("temporary roots must agree")
		}
	}
	samples := m4Int(t, "PULSE_M4_PERFORMANCE_SAMPLES", 20, 10000)
	concurrency := m4Int(t, "PULSE_M4_PERFORMANCE_CONCURRENCY", 1, 32)
	binary := os.Getenv("PULSE_M4_PERFORMANCE_BINARY")
	if !strings.HasPrefix(binary, "/data/temp/pulse-m4-performance.") {
		t.Fatal("private optimized production binary required")
	}
	info, e := buildinfo.ReadFile(binary)
	m4Must(t, e)
	for _, setting := range info.Settings {
		if setting.Key == "-race" && setting.Value == "true" {
			t.Fatal("production binary has race overhead")
		}
	}
	t.Logf("M4_BINARY Go=%s path=cmd/pulsemedia optimized_default=true race=false", info.GoVersion)
	ctx, cancel := context.WithTimeout(context.Background(), 23*time.Minute)
	t.Cleanup(cancel)
	pool, dsn := bootstrapTestPool(t)
	data := t.TempDir()
	gen := idgen.New()
	m4Must(t, ensureDefaultLibraryRoots(ctx, pool, gen, data))
	q := db.New(pool)
	root, e := q.GetFirstOnlineRootForType(ctx, "movie")
	m4Must(t, e)
	key, e := crypto.EnsureMasterKey(data, "")
	m4Must(t, e)
	user, e := q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "benchmark-admin", PasswordHash: "unused-session-only-benchmark", Role: ports.RoleAdmin, IsActive: true})
	m4Must(t, e)
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: user.ID.String(), Role: ports.RoleAdmin}
	token, hash, e := crypto.GenerateToken("pm_api_")
	m4Must(t, e)
	_, e = q.CreateAPIToken(ctx, db.CreateAPITokenParams{ID: uuid.New(), UserID: user.ID, Name: "BENCHMARK read only", TokenHash: hash, Scopes: []byte(`["read"]`), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	m4Must(t, e)
	var endpointCalls atomic.Int64
	ss := search.NewService(postgres.NewSearchExecutionStore(pool), gen, crypto.NewSecretProtector(key))
	endpointIDs := make([]uuid.UUID, 0, 10)
	for n := 0; n < 10; n++ {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			endpointCalls.Add(1)
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Query().Get("t") == "caps" {
				_, _ = io.WriteString(w, `<caps><server title="BENCHMARK synthetic"/><limits max="100" default="50"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q,imdbid,tmdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
				return
			}
			_, _ = io.WriteString(w, `<rss><channel><response offset="0" total="0"/></channel></rss>`)
		}))
		t.Cleanup(server.Close)
		idx, err := ss.CreateIndexer(ctx, fmt.Sprintf("BENCHMARK synthetic %02d", n), server.URL, "benchmark-local-only", nil)
		m4Must(t, err)
		endpointIDs = append(endpointIDs, idx.ID)
	}
	subSvcStore := postgres.NewSubscriptionIntentStore(pool, gen)
	subSvc := subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(pool, gen), subSvcStore)
	for n := 0; n < m4SubscriptionCount; n++ {
		_, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: m4ID(1, n), Title: fmt.Sprintf("BENCHMARK movie %04d", n), MediaType: "movie"})
		m4Must(t, e)
		_, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: m4ID(2, n), MediaID: m4ID(1, n), UnitKey: "movie"})
		m4Must(t, e)
		sub, wanted, err := subSvc.CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{MediaID: m4ID(1, n), RootID: root.ID, ScopeType: "all", IndexerIDs: endpointIDs})
		m4Must(t, err)
		if len(wanted) != 1 {
			t.Fatal("subscription did not persist wanted scope")
		}
		_, err = subSvc.UpdateState(ctx, actor, sub.ID, "paused")
		m4Must(t, err)
	}
	t.Log("M4_PHASE subscriptions persisted via production service; creating actual tiny local files")
	start := time.Now()
	rows := make([][]any, 0, m4FileCount)
	for n := 0; n < m4FileCount; n++ {
		rel := fmt.Sprintf("chunk-%03d/%06d.mkv", n/1000, n)
		absolute := filepath.Join(root.Path, rel)
		if n%1000 == 0 {
			m4Must(t, os.MkdirAll(filepath.Dir(absolute), 0700))
		}
		m4Must(t, os.WriteFile(absolute, []byte("BENCHMARK synthetic video bytes\n"), 0600))
		st, err := os.Stat(absolute)
		m4Must(t, err)
		sys := st.Sys().(*syscall.Stat_t)
		fingerprint := fmt.Sprintf("stat.v1:%d:%d:%d", sys.Ino, st.Size(), st.ModTime().UnixNano())
		rows = append(rows, []any{m4ID(3, n), root.ID, rel, st.Size(), st.ModTime().UnixNano(), fingerprint})
	}
	copied, e := pool.CopyFrom(ctx, pgx.Identifier{"library_file"}, []string{"id", "root_id", "rel_path", "size_bytes", "mtime_ns", "file_fingerprint"}, pgx.CopyFromRows(rows))
	m4Must(t, e)
	if copied != m4FileCount {
		t.Fatal("incomplete file seed")
	}
	tag, e := pool.Exec(ctx, `INSERT INTO library_file_unit(file_id,unit_id) SELECT id,('00000002-0000-4000-8000-'||lpad(((split_part(split_part(rel_path,'/',2),'.',1)::integer%1000)+1)::text,12,'0'))::uuid FROM library_file WHERE root_id=$1`, root.ID)
	m4Must(t, e)
	if tag.RowsAffected() != m4FileCount {
		t.Fatal("incomplete mapping seed")
	}
	t.Logf("M4_PHASE fixture_creation_seconds=%.3f files=%d actual_bytes_each=32 copied_with_constraints=true", time.Since(start).Seconds(), copied)
	observer := &m4ScanObserver{ScanRepository: postgres.NewScanObservationStore(pool), pool: pool}
	scanStart := time.Now()
	report, e := library.NewScanner(gen, rootfs.NewFilesystem(), observer).ScanRoot(ctx, root.ID)
	m4Must(t, e)
	if report.State != "succeeded" || report.FilesObserved != m4FileCount || report.MissingCandidates != 0 || observer.rows != m4FileCount || observer.calls != 782 {
		t.Fatalf("unexpected real scan evidence: %+v pages=%d rows=%d", report, observer.calls, observer.rows)
	}
	t.Logf("M4_SCAN seconds=%.3f observed=%d reconciliation_calls=%d reconciled_rows=%d max_rows_per_call=%d missing=%d generator_process=true", time.Since(scanStart).Seconds(), report.FilesObserved, observer.calls, observer.rows, observer.maxRows, report.MissingCandidates)
	// Keep normal schedulers enabled; the completed scan and paused intents describe
	// an idle library. Fresh startup may schedule a validating rescan, awaited below.
	_, e = pool.Exec(ctx, "ANALYZE")
	m4Must(t, e)
	m4Inventory(t, ctx, pool)
	m4Environment(t, ctx, pool)

	listener, e := net.Listen("tcp", "127.0.0.1:0")
	m4Must(t, e)
	addr := listener.Addr().String()
	m4Must(t, listener.Close())
	configPath := filepath.Join(data, "benchmark.yaml")
	m4Must(t, os.WriteFile(configPath, []byte("{}\n"), 0600))
	logFile, e := os.OpenFile(filepath.Join(data, "production.log"), os.O_CREATE|os.O_WRONLY, 0600)
	m4Must(t, e)
	defer func() { _ = logFile.Close() }()
	cmd := exec.CommandContext(ctx, binary, "-c", configPath, "-data-dir", data, "-http-addr", addr, "-log-level", "error")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + data, "TMPDIR=" + scratch, "TMP=" + scratch, "TEMP=" + scratch, "PULSE_DATABASE_URL=" + dsn}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	m4Must(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("production shutdown: %v", err)
			}
		case <-time.After(50 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("production shutdown exceeded drain budget")
		}
		t.Log("M4_OWNED_PRODUCTION_PROCESS_JOINED")
	}
	t.Cleanup(stop)
	tr := &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 32, MaxConnsPerHost: 32, Proxy: nil}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	defer tr.CloseIdleConnections()
	base := "http://" + addr
	ready := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		r, err := client.Get(base + "/readyz")
		if err == nil {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("production startup failed: %v (private log suppressed)", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("production readiness timeout (private log suppressed)")
	}
	// Reach actual idle after any startup scan. No worker or validation is disabled.
	for deadline := time.Now().Add(10 * time.Minute); ; {
		var n int
		m4Must(t, pool.QueryRow(ctx, `SELECT count(*) FROM job WHERE state IN ('queued','running') AND kind='library_scan'`).Scan(&n))
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup scans did not drain")
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("M4_METHOD actor=current-admin credential=API-token scopes=read network=loopback-http latency=through-body-read nearest_rank=true samples_per_route=%d warmup_per_route=5 concurrency=%d cold=first-request-after-process-start PG_OS_caches=warm_not_dropped production_race=false Go_heap=unmeasured", samples, concurrency)
	routes := []struct {
		name, path string
		count      int
	}{
		{"subscriptions_all", "/api/v1/subscriptions", 1000},
		{"catalog_first_50", "/api/v1/library/catalog?limit=50", 50},
		{"indexers_10", "/api/v1/indexers", 10},
	}
	firstPage := httpapi.LibraryCatalogPage{}
	for _, route := range routes {
		raw, d, err := m4GET(client, base+route.path, token, 200)
		m4Must(t, err)
		m4Count(t, raw, route.count)
		t.Logf("M4_COLD route=%s n=1 milliseconds=%.3f bytes=%d", route.name, float64(d)/float64(time.Millisecond), len(raw))
		if route.name == "catalog_first_50" {
			m4Must(t, json.Unmarshal(raw, &firstPage))
		}
	}
	if firstPage.NextCursor == nil || *firstPage.NextCursor == "" {
		t.Fatal("missing continuation")
	}
	continuation := "/api/v1/library/catalog?limit=50&cursor=" + url.QueryEscape(*firstPage.NextCursor)
	raw, _, e := m4GET(client, base+continuation, token, 200)
	m4Must(t, e)
	m4Count(t, raw, 50)
	var secondPage httpapi.LibraryCatalogPage
	m4Must(t, json.Unmarshal(raw, &secondPage))
	seen := map[uuid.UUID]bool{}
	for _, v := range firstPage.Items {
		seen[v.FileId] = true
	}
	for _, v := range secondPage.Items {
		if seen[v.FileId] {
			t.Fatal("cursor duplicates")
		}
	}
	routes = append(routes, struct {
		name, path string
		count      int
	}{"catalog_next_50", continuation, 50})
	// A deterministic tail cursor exercises exhaustion without walking 2000 pages.
	tail, _ := json.Marshal(map[string]any{"Actor": actor.ID, "Role": string(actor.Role), "File": m4ID(3, m4FileCount-51), "Unit": m4ID(2, (m4FileCount-51)%1000)})
	tailCursor := base64.RawURLEncoding.EncodeToString(tail)
	raw, _, e = m4GET(client, base+"/api/v1/library/catalog?limit=50&cursor="+url.QueryEscape(tailCursor), token, 200)
	m4Must(t, e)
	m4Count(t, raw, 50)
	var tailPage httpapi.LibraryCatalogPage
	m4Must(t, json.Unmarshal(raw, &tailPage))
	if tailPage.NextCursor != nil && *tailPage.NextCursor != "" {
		t.Fatal("tail was not exhausted")
	}
	for _, path := range []string{"/api/v1/library/catalog?limit=101", "/api/v1/library/catalog?limit=0", "/api/v1/library/catalog?cursor=malformed"} {
		_, _, err := m4GET(client, base+path, token, 400)
		m4Must(t, err)
	}
	_, _, e = m4GET(client, base+"/api/v1/library/catalog?limit=50", "", 401)
	m4Must(t, e)
	t.Log("M4_PAGE_CHECK first=50 next=50 duplicates=0 tail=50 exhausted=true invalid_limit_and_cursor=400 unauthenticated=401 subscriptions=unpaginated_1000 indexers=unpaginated_10")
	for _, route := range routes {
		for n := 0; n < 5; n++ {
			_, _, err := m4GET(client, base+route.path, token, 200)
			m4Must(t, err)
		}
		m4Measure(t, client, base+route.path, token, route.name, samples, 1, route.count)
		m4Measure(t, client, base+route.path, token, route.name, samples, concurrency, route.count)
	}
	// Bounded quiet interval, followed by production-process RSS samples. Reading
	// /proc/<child>/status avoids reporting this generator's Go runtime as the app.
	time.Sleep(10 * time.Second)
	rss := make([]time.Duration, 0, 10)
	for n := 0; n < 10; n++ {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
		m4Must(t, err)
		found := false
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				m4Must(t, err)
				rss = append(rss, time.Duration(kb*1024))
				found = true
			}
		}
		if !found {
			t.Fatal("production RSS unavailable")
		}
		time.Sleep(time.Second)
	}
	sort.Slice(rss, func(i, j int) bool { return rss[i] < rss[j] })
	t.Logf("M4_IDLE production_pid=%d RSS_samples=10 interval_seconds=1 quiet_seconds=10 RSS_min_MiB=%.3f RSS_median_MiB=%.3f RSS_max_MiB=%.3f Go_heap=unmeasured target_Go_idle_MB=100", cmd.Process.Pid, float64(rss[0])/(1<<20), float64(rss[4])/(1<<20), float64(rss[9])/(1<<20))
	stop()
	m4Inventory(t, ctx, pool)
	m4Fairness(t, ctx, pool)
	t.Logf("M4_ENDPOINT observed_requests=%d configured_synthetic=10 real_source_proof=false", endpointCalls.Load())
	t.Log("M4_LIMITS paused_movie_subscriptions=1000 copies_per_movie=100 tiny_local_files=true payload_downloads=false posters_and_TV=false scan_timings=generator_process missing_reconciliation_and_fault_resume=not_covered queue_sustained_load=not_covered Go_heap=unmeasured M4_01_dependency=blocked M4_04_acceptance=not_claimed")
}

func m4GET(client *http.Client, path, token string, status int) ([]byte, time.Duration, error) {
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	r, err := client.Do(req)
	if err != nil {
		return nil, time.Since(start), err
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	elapsed := time.Since(start)
	if err != nil {
		return nil, elapsed, err
	}
	if r.StatusCode != status {
		return nil, elapsed, fmt.Errorf("HTTP status %d, expected %d", r.StatusCode, status)
	}
	return body, elapsed, nil
}
func m4Count(t *testing.T, raw []byte, n int) {
	t.Helper()
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	m4Must(t, json.Unmarshal(raw, &page))
	if len(page.Items) != n {
		t.Fatalf("HTTP item count=%d expected=%d", len(page.Items), n)
	}
}
func m4Measure(t *testing.T, client *http.Client, path, token, name string, n, concurrency, count int) {
	t.Helper()
	ds := make([]time.Duration, n)
	errs := make(chan error, n)
	work := make(chan int, n)
	for i := 0; i < n; i++ {
		work <- i
	}
	close(work)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				raw, d, err := m4GET(client, path, token, 200)
				ds[j] = d
				if err == nil {
					var p struct {
						Items []json.RawMessage `json:"items"`
					}
					err = json.Unmarshal(raw, &p)
					if err == nil && len(p.Items) != count {
						err = fmt.Errorf("response count %d expected %d", len(p.Items), count)
					}
				}
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	quant := func(p float64) float64 {
		return float64(ds[int(math.Ceil(p*float64(n)))-1]) / float64(time.Millisecond)
	}
	t.Logf("M4_HTTP route=%s n=%d concurrency=%d p50_ms=%.3f p95_ms=%.3f p99_ms=%.3f max_ms=%.3f requests_per_second=%.3f p95_goal_lt_200ms=%t", name, n, concurrency, quant(.50), quant(.95), quant(.99), quant(1), float64(n)/elapsed.Seconds(), quant(.95) < 200)
}
func m4Inventory(t *testing.T, ctx context.Context, p *pgxpool.Pool) {
	t.Helper()
	var subs, wanted, files, mappings, endpoints, paused, baseline int
	m4Must(t, p.QueryRow(ctx, `SELECT (SELECT count(*) FROM subscription),(SELECT count(*) FROM wanted_item),(SELECT count(*) FROM library_file WHERE retired_at IS NULL),(SELECT count(*) FROM library_file_unit WHERE retired_at IS NULL),(SELECT count(*) FROM indexer),(SELECT count(*) FROM subscription WHERE state='paused'),(SELECT count(*) FROM library_root WHERE media_type='movie' AND baseline_completed)`).Scan(&subs, &wanted, &files, &mappings, &endpoints, &paused, &baseline))
	if subs != 1000 || wanted != 1000 || files != 100000 || mappings != 100000 || endpoints != 10 || paused != 1000 || baseline != 1 {
		t.Fatalf("inventory mismatch subscriptions=%d wanted=%d files=%d mappings=%d endpoints=%d paused=%d baseline=%d", subs, wanted, files, mappings, endpoints, paused, baseline)
	}
	t.Logf("M4_INVENTORY subscriptions=%d wanted=%d active_indexed_files=%d active_mappings=%d configured_endpoints=%d paused=%d movie_baseline=%d", subs, wanted, files, mappings, endpoints, paused, baseline)
}
func m4Environment(t *testing.T, ctx context.Context, p *pgxpool.Pool) {
	t.Helper()
	settings := []string{"server_version", "fsync", "synchronous_commit", "full_page_writes", "wal_level", "shared_buffers", "work_mem", "maintenance_work_mem", "max_connections", "max_wal_size", "effective_cache_size", "max_parallel_workers", "jit"}
	for _, name := range settings {
		var value string
		m4Must(t, p.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&value))
		t.Logf("M4_PG %s=%s", name, value)
		if (name == "fsync" || name == "full_page_writes") && value != "on" {
			t.Fatal("durability disabled")
		}
		if name == "synchronous_commit" && value != "on" {
			t.Fatal("synchronous commit disabled")
		}
		if name == "wal_level" && value != "replica" && value != "logical" {
			t.Fatal("WAL level below replica")
		}
	}
	var dbBytes int64
	m4Must(t, p.QueryRow(ctx, "SELECT pg_database_size(current_database())").Scan(&dbBytes))
	t.Logf("M4_ENV Go=%s OS=%s arch=%s logical_CPUs=%d harness_GOMAXPROCS=%d database_bytes=%d production_GOMAXPROCS=Go_default production_GOMEMLIMIT=Go_default", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), dbBytes)
	for _, path := range []string{"/proc/cpuinfo", "/proc/meminfo", "/proc/loadavg", "/sys/fs/cgroup/cpu.max", "/sys/fs/cgroup/memory.max"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Logf("M4_HOST %s=unavailable", path)
			continue
		}
		text := strings.TrimSpace(string(raw))
		if path == "/proc/cpuinfo" {
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "model name") {
					text = line
					break
				}
			}
		}
		if path == "/proc/meminfo" {
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					text = line
					break
				}
			}
		}
		t.Logf("M4_HOST %s=%s", path, text)
	}
}
func m4Fairness(t *testing.T, ctx context.Context, p *pgxpool.Pool) {
	t.Helper()
	q := jobs.NewQueue(p, idgen.New(), clock.New())
	var now time.Time
	m4Must(t, p.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now))
	low, e := q.Enqueue(ctx, "m4_benchmark_fairness", "", -2147483648, nil, now.Add(-6*time.Minute), 3)
	m4Must(t, e)
	for n := 0; n < 8; n++ {
		_, e = q.Enqueue(ctx, "m4_benchmark_fairness", "", 2147483647, nil, now, 3)
		m4Must(t, e)
	}
	start := time.Now()
	job, e := q.Claim(ctx, []string{"m4_benchmark_fairness"}, "benchmark-owner", time.Minute)
	m4Must(t, e)
	if job == nil || job.ID != low.ID || job.Priority != low.Priority {
		t.Fatal("aged job was starved")
	}
	m4Must(t, q.Finish(ctx, job.ID, "benchmark-owner", job.LeaseToken, nil))
	for n := 0; n < 8; n++ {
		job, e = q.Claim(ctx, []string{"m4_benchmark_fairness"}, "benchmark-owner", time.Minute)
		m4Must(t, e)
		if job == nil {
			t.Fatal("lost queued priority job")
		}
		m4Must(t, q.Finish(ctx, job.ID, "benchmark-owner", job.LeaseToken, nil))
	}
	t.Logf("M4_QUEUE aged_low_rank=1 fresh_high_jobs=8 completed=9 elapsed_ms=%.3f persistent_owner_token_finish=true sustained_fairness=unmeasured", float64(time.Since(start))/float64(time.Millisecond))
}
