package search_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	// This external integration fixture composes the native adapter with real PG.
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab" //nolint:depguard // Test-only native HTTP composition.
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type aggregateFanoutGate struct {
	ch   chan struct{}
	once sync.Once
}

func newAggregateFanoutGate() *aggregateFanoutGate {
	return &aggregateFanoutGate{ch: make(chan struct{})}
}

func (g *aggregateFanoutGate) release() { g.once.Do(func() { close(g.ch) }) }

type aggregateFanoutCall struct {
	done    chan struct{}
	rows    []ports.SearchCandidate
	partial bool
	err     error
	cancel  context.CancelFunc
}

func startAggregateFanout(t *testing.T, svc *search.Service, unblock func()) *aggregateFanoutCall {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	call := &aggregateFanoutCall{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(call.done)
		call.rows, call.partial, call.err = svc.AggregateSearchDetailed(ctx, "Fanout Movie")
	}()
	t.Cleanup(func() {
		cancel()
		unblock()
		awaitAggregateFanout(t, call.done)
	})
	return call
}

func awaitAggregateFanout(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("aggregate/HTTP work did not terminate")
	}
}

// Reuse the real PG fixture, then construct the tested service with its default
// two-second budget. Each endpoint is fresh and is queried only once.
func aggregateFanoutFixture(t *testing.T, server *httptest.Server, count int) (*search.Service, *pgxpool.Pool, []uuid.UUID) {
	t.Helper()
	_, pool, q, key, unused := setupSearchTest(t)
	if err := q.DeleteIndexer(context.Background(), unused); err != nil {
		t.Fatal(err)
	}
	svc := search.NewService(postgres.NewSearchExecutionStore(pool), idgen.New(), crypto.NewSecretProtector(key))
	ids := make([]uuid.UUID, count)
	for i := range ids {
		url := fmt.Sprintf("%s/%d", server.URL, i)
		idx, err := svc.CreateIndexer(context.Background(), fmt.Sprintf("Fanout %d", i), url, "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = idx.ID
		svc.RegisterClient(idx.ID, torznab.NewClient(url, "fixture", &http.Client{Timeout: 10 * time.Second}))
	}
	return svc, pool, ids
}

func writeAggregateFanoutPage(w http.ResponseWriter, source string, empty bool) {
	w.Header().Set("Content-Type", "application/xml")
	if empty {
		_, _ = fmt.Fprint(w, `<rss><channel><response offset="0" total="0"/></channel></rss>`)
		return
	}
	_, _ = fmt.Fprintf(w, `<rss><channel><response offset="0" total="1"/><item><title>Fanout.Movie.2026.1080p.WEB-DL</title><guid>fanout-%s</guid><link>https://fixture.invalid/file</link></item></channel></rss>`, source)
}

func TestAggregateFanoutSlowFirstHealthyPersistsAnd429IsPartial(t *testing.T) {
	slow := newAggregateFanoutGate()
	entered := make(chan struct{})
	healthy := make(chan struct{})
	limited := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/0/api":
			close(entered)
			select {
			case <-slow.ch:
			case <-r.Context().Done():
				return
			}
		case "/1/api":
			close(healthy)
		case "/2/api":
			close(limited)
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeAggregateFanoutPage(w, strings.Split(r.URL.Path, "/")[1], false)
	}))
	t.Cleanup(server.Close)
	svc, pool, ids := aggregateFanoutFixture(t, server, 3)
	call := startAggregateFanout(t, svc, slow.release)
	awaitAggregateFanout(t, entered)
	awaitAggregateFanout(t, healthy)
	awaitAggregateFanout(t, limited)

	// Observe durable completion while the first native HTTP request is held.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var persisted bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM indexer_query q JOIN indexer_query_candidate qc ON qc.query_id=q.id JOIN release_candidate c ON c.id=qc.candidate_id WHERE q.indexer_id=$1 AND q.state='succeeded' AND c.guid='fanout-1')`, ids[1]).Scan(&persisted)
		if err != nil {
			t.Fatal(err)
		}
		if persisted {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("healthy endpoint did not persist while slow endpoint was held")
		case <-ticker.C:
		}
	}
	select {
	case <-call.done:
		t.Fatal("aggregate returned before its slow HTTP call finished")
	default:
	}
	slow.release()
	awaitAggregateFanout(t, call.done)
	if call.err != nil || !call.partial || len(call.rows) != 2 || call.rows[0].IndexerID != ids[0] || call.rows[1].IndexerID != ids[1] {
		t.Fatalf("expected deterministic partial success: rows=%v partial=%v err=%v", call.rows, call.partial, call.err)
	}
	var backedOff bool
	if err := pool.QueryRow(context.Background(), "SELECT next_query_at>clock_timestamp()+interval '40 seconds' FROM indexer WHERE id=$1", ids[2]).Scan(&backedOff); err != nil || !backedOff {
		t.Fatalf("429 budget not preserved: backoff=%v err=%v", backedOff, err)
	}
}

func TestAggregateFanoutSevenSourcesBoundAndOrder(t *testing.T) {
	gate := newAggregateFanoutGate()
	started := make(chan struct{}, 7)
	var active, peak, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-gate.ch:
		case <-r.Context().Done():
			return
		}
		writeAggregateFanoutPage(w, strings.Split(r.URL.Path, "/")[1], false)
	}))
	t.Cleanup(server.Close)
	svc, _, ids := aggregateFanoutFixture(t, server, 7)
	call := startAggregateFanout(t, svc, gate.release)
	for range 4 {
		awaitAggregateFanout(t, started)
	}
	select {
	case <-started:
		t.Fatal("more than four endpoints started while first four were held")
	case <-time.After(100 * time.Millisecond):
	}
	gate.release()
	awaitAggregateFanout(t, call.done)
	if call.err != nil || call.partial || len(call.rows) != 7 || calls.Load() != 7 || peak.Load() != 4 || active.Load() != 0 {
		t.Fatalf("bound/results: rows=%d partial=%v calls=%d peak=%d active=%d err=%v", len(call.rows), call.partial, calls.Load(), peak.Load(), active.Load(), call.err)
	}
	for i, row := range call.rows {
		if row.IndexerID != ids[i] || row.Guid != "fanout-"+strconv.Itoa(i) {
			t.Fatalf("completion order changed endpoint order at %d: %+v", i, row)
		}
	}
}

func TestAggregateFanoutQueuedSourcesKeepFrozenVersionAndHealth(t *testing.T) {
	gate := newAggregateFanoutGate()
	started := make(chan struct{}, 6)
	var calls [6]atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, _ := strconv.Atoi(strings.Split(r.URL.Path, "/")[1])
		calls[i].Add(1)
		started <- struct{}{}
		select {
		case <-gate.ch:
		case <-r.Context().Done():
			return
		}
		writeAggregateFanoutPage(w, strconv.Itoa(i), false)
	}))
	t.Cleanup(server.Close)
	svc, pool, ids := aggregateFanoutFixture(t, server, 6)
	call := startAggregateFanout(t, svc, gate.release)
	for range 4 {
		awaitAggregateFanout(t, started)
	}
	// Both changed endpoints are still queued behind four held physical calls.
	if _, err := pool.Exec(context.Background(), "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", ids[4]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), "UPDATE indexer SET health='offline' WHERE id=$1", ids[5]); err != nil {
		t.Fatal(err)
	}
	gate.release()
	awaitAggregateFanout(t, call.done)
	if call.err != nil || !call.partial || len(call.rows) != 4 || calls[4].Load() != 0 || calls[5].Load() != 0 {
		t.Fatalf("queued source changed contract: rows=%d partial=%v changedCalls=%d offlineCalls=%d err=%v", len(call.rows), call.partial, calls[4].Load(), calls[5].Load(), call.err)
	}
	for i, row := range call.rows {
		if row.IndexerID != ids[i] {
			t.Fatalf("queued source leaked into result: %+v", row)
		}
	}
}

func TestAggregateFanoutNativeEmptyAndFailureDistinctions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status [2]int
		failed bool
	}{
		{"all_failed", [2]int{http.StatusServiceUnavailable, http.StatusTooManyRequests}, true},
		{"complete_empty", [2]int{http.StatusOK, http.StatusOK}, false},
		{"empty_plus_failed", [2]int{http.StatusOK, http.StatusTooManyRequests}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i, _ := strconv.Atoi(strings.Split(r.URL.Path, "/")[1])
				if tc.status[i] != http.StatusOK {
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(tc.status[i])
					return
				}
				writeAggregateFanoutPage(w, "", true)
			}))
			t.Cleanup(server.Close)
			svc, _, _ := aggregateFanoutFixture(t, server, 2)
			call := startAggregateFanout(t, svc, func() {})
			awaitAggregateFanout(t, call.done)
			if len(call.rows) != 0 || call.partial || (tc.failed && !errors.Is(call.err, ports.ErrUnavailable)) || (!tc.failed && call.err != nil) {
				t.Fatalf("empty/failure distinction: rows=%d partial=%v err=%v", len(call.rows), call.partial, call.err)
			}
		})
	}
}

func TestAggregateFanoutCancellationJoinsNativeHTTPAndStopsQueuedSources(t *testing.T) {
	started := make(chan struct{}, 7)
	exited := make(chan struct{}, 7)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
		exited <- struct{}{}
	}))
	t.Cleanup(server.Close)
	svc, pool, ids := aggregateFanoutFixture(t, server, 7)
	call := startAggregateFanout(t, svc, func() {})
	for range 4 {
		awaitAggregateFanout(t, started)
	}
	call.cancel()
	awaitAggregateFanout(t, call.done)
	for range 4 {
		awaitAggregateFanout(t, exited)
	}
	if !errors.Is(call.err, context.Canceled) || len(call.rows) != 0 || calls.Load() != 4 {
		t.Fatalf("cancel did not stop/join workers: rows=%d calls=%d err=%v", len(call.rows), calls.Load(), call.err)
	}
	for _, id := range ids {
		release, err := postgres.AcquireIndexerPermit(context.Background(), pool, id)
		if err != nil {
			t.Fatal("physical permit remained held after join", err)
		}
		release()
	}
}

func TestAggregateFanoutCancellationRetainsIgnoringClientPhysicalSlot(t *testing.T) {
	_, pool, _, key, idx := setupSearchTest(t)
	svc := search.NewService(postgres.NewSearchExecutionStore(pool), idgen.New(), crypto.NewSecretProtector(key))
	slow := &blockedEndpoint{entered: make(chan struct{}), release: make(chan struct{})}
	svc.RegisterClient(idx, slow)
	gate := newAggregateFanoutGate()
	slow.release = gate.ch
	call := startAggregateFanout(t, svc, gate.release)
	awaitAggregateFanout(t, slow.entered)
	call.cancel()
	select {
	case <-call.done:
		t.Fatal("aggregate abandoned physical I/O after cancellation")
	case <-time.After(100 * time.Millisecond):
	}
	release, err := postgres.AcquireIndexerPermit(context.Background(), pool, idx)
	if err == nil {
		release()
		t.Fatal("cancellation released physical exclusion")
	}
	if !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	other := search.NewService(postgres.NewSearchExecutionStore(pool), idgen.New(), crypto.NewSecretProtector(key))
	other.RegisterClient(idx, &mockIndexerClient{})
	if _, err = other.ExecuteSearch(context.Background(), idx, "overlap", 0); !errors.Is(err, ports.ErrUnavailable) || slow.calls.Load() != 1 {
		t.Fatalf("overlapping physical call admitted: calls=%d err=%v", slow.calls.Load(), err)
	}
	gate.release()
	awaitAggregateFanout(t, call.done)
	if !errors.Is(call.err, context.Canceled) || len(call.rows) != 0 {
		t.Fatalf("cancel result: rows=%d err=%v", len(call.rows), call.err)
	}
	release, err = postgres.AcquireIndexerPermit(context.Background(), pool, idx)
	if err != nil {
		t.Fatal("physical permit not released after I/O returned", err)
	}
	release()
}
