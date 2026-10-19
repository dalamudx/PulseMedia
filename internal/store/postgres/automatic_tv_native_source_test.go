package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

type automaticTVNativeTransport struct {
	target             *url.URL
	ignoreCancellation bool
}

func (r automaticTVNativeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.Host != "tv-source.test" {
		return nil, ports.ErrBadResponse
	}
	ctx := request.Context()
	if r.ignoreCancellation {
		ctx = context.WithoutCancel(ctx)
	}
	clone := request.Clone(ctx)
	address := *request.URL
	address.Scheme, address.Host = r.target.Scheme, r.target.Host
	clone.URL, clone.Host = &address, r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func nativeAutomaticTVService(t *testing.T, f automaticTVFixture, server *httptest.Server, ignoreCancellation bool) *search.Service {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	service := search.NewService(f.execution, idgen.New(), codec, 0)
	service.SetAutomaticTVRoundRepository(f.repo)
	service.RegisterClient(f.indexer, torznab.NewClient("https://tv-source.test/api?apikey=private-query-key", "fixture-api-key", &http.Client{Transport: automaticTVNativeTransport{target: target, ignoreCancellation: ignoreCancellation}, Timeout: 10 * time.Second}))
	return service
}

func respondNativeAutomaticTV(t *testing.T, response http.ResponseWriter, request *http.Request) {
	t.Helper()
	query := request.URL.Query()
	if request.URL.Path != "/api" || query.Get("t") != "tvsearch" || query.Get("q") != "Trusted Native Series 2024" || query.Get("season") != "2" || query.Get("ep") != "3" || query.Get("offset") != "0" || query.Get("limit") != "50" || query.Get("cat") != "5000,5040" || query.Get("apikey") != "fixture-api-key" {
		t.Error("native TV request did not match frozen catalog, episode and pagination")
		http.Error(response, "invalid native query", http.StatusBadRequest)
		return
	}
	response.Header().Set("Content-Type", "application/rss+xml")
	_, err := fmt.Fprint(response, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="1"/><item><title>Trusted.Native.Series.2024.S02E03.1080p.WEB-DL.H264</title><guid>native-tv-episode</guid><enclosure url="https://tv-source.test/download?id=episode&amp;token=private-reference" length="1234"/><torznab:attr name="seeders" value="9"/></item></channel></rss>`)
	if err != nil {
		t.Error("native response write failed")
	}
}

func TestAutomaticTVSourceNativeQueryProofPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	round := f.start(t)
	work := f.load(t, round)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		respondNativeAutomaticTV(t, w, r)
	}))
	t.Cleanup(server.Close)
	service := nativeAutomaticTVService(t, f, server, false)
	if err := service.ExecuteAutomaticTVSource(t.Context(), work.Task); err != nil {
		t.Fatal("execute", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("native requests=%d", calls.Load())
	}
	if intentCount(t, f.pool, `SELECT count(*) FROM indexer_query WHERE query_type='tv_episode' AND state='succeeded'`) != 1 || intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_query_candidate`) != 1 || intentCount(t, f.pool, `SELECT count(*) FROM indexer_query_page WHERE offset_verified AND item_count=1 AND total=1`) != 1 {
		t.Fatal("native source did not atomically publish query, candidate and page proof")
	}
	var state, jobState, keyword string
	if err := f.pool.QueryRow(t.Context(), `SELECT s.state,j.state,q.keyword FROM automatic_tv_source s JOIN job j ON j.id=s.job_id JOIN automatic_tv_query a ON a.round_id=s.round_id AND a.indexer_id=s.indexer_id JOIN indexer_query q ON q.id=a.query_id WHERE s.round_id=$1`, round.ID).Scan(&state, &jobState, &keyword); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || jobState != "succeeded" || !strings.HasPrefix(keyword, search.TVEpisodeQuerySchemaVersion+":") {
		t.Fatalf("source=%s job=%s keyword identity valid=%t", state, jobState, strings.HasPrefix(keyword, search.TVEpisodeQuerySchemaVersion+":"))
	}
	if intentCount(t, f.pool, `SELECT count(*) FROM acquisition_reservation`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM unit_reservation`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM download_requirement`) != 0 {
		t.Fatal("source observation created acquisition permission")
	}
	if round.Snapshot.MappingMode != "standard" || round.Snapshot.MappingRevision != 0 {
		t.Fatal("fixture must retain implicit standard numbering")
	}
	lease := tvExecutionLease(t, f, round)
	finalizer := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
	result, err := finalizer.FinalizeAutomaticTVRound(t.Context(), round.ID, lease)
	if err != nil || result == nil || result.State != "reserved" {
		t.Fatalf("native default standard finalization: result=%+v error=%v", result, err)
	}
	tvExecutionCounts(t, f, 1)
	stable := tvExecutionState(t, f)
	recovered, err := finalizer.FinalizeAutomaticTVRound(t.Context(), round.ID, lease)
	if err != nil || recovered == nil || recovered.OperationID != result.OperationID || tvExecutionState(t, f) != stable {
		t.Fatal("original finalize recovery changed the protected operation", err)
	}
}

func TestAutomaticTVSourceNativeCancellationRetainsPhysicalPermitPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	round := f.start(t)
	work := f.load(t, round)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		respondNativeAutomaticTV(t, w, r)
	}))
	t.Cleanup(func() { unblock(); server.Close() })
	service := nativeAutomaticTVService(t, f, server, true)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.ExecuteAutomaticTVSource(ctx, work.Task) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		unblock()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("source I/O did not join")
			}
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native request not entered")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		t.Fatalf("cancel detached native I/O: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	admission, permit, err := f.execution.AdmitQuery(t.Context(), automaticTVSpec(t, work, 0))
	if permit != nil {
		permit.Close()
		t.Fatal("cancel released physical permit early")
	}
	if !errors.Is(err, ports.ErrAutomaticTVSearchDeferred) || admission.Shared != nil {
		t.Fatalf("second source admission should defer: %v", err)
	}
	unblock()
	select {
	case err = <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel classification: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("native source failed to join")
	}
	if intentCount(t, f.pool, `SELECT count(*) FROM release_candidate`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_query_candidate`) != 0 || intentCount(t, f.pool, `SELECT count(*) FROM indexer_query WHERE state='succeeded'`) != 0 {
		t.Fatal("late success published a candidate")
	}
	physical, err := postgres.AcquireIndexerPermit(t.Context(), f.pool, f.indexer)
	if err != nil {
		t.Fatal("physical permit was not released after joined I/O", err)
	}
	physical()
}

func TestAutomaticTVActiveEvaluationRoundPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	schedule := postgres.NewEvaluationScheduleStore(f.pool)
	active, err := schedule.HasActiveEvaluationRound(t.Context(), f.wanted)
	if err != nil || active {
		t.Fatalf("before round: active=%t error=%v", active, err)
	}
	round := f.start(t)
	active, err = schedule.HasActiveEvaluationRound(t.Context(), f.wanted)
	if err != nil || !active {
		t.Fatalf("during round: active=%t error=%v", active, err)
	}
	execIntent(t, f.pool, `UPDATE automatic_tv_round SET state='closed' WHERE id=$1`, round.ID)
	active, err = schedule.HasActiveEvaluationRound(t.Context(), f.wanted)
	if err != nil || active {
		t.Fatalf("after round: active=%t error=%v", active, err)
	}
	f.pool.Close()
	if _, err = schedule.HasActiveEvaluationRound(t.Context(), f.wanted); err == nil {
		t.Fatal("database failure became an absent round")
	}
}
