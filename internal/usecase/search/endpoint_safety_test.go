package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"sync/atomic"
	"testing"
	"time"
)

type blockedEndpoint struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

func (b *blockedEndpoint) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	b.calls.Add(1)
	close(b.entered)
	<-b.release
	return []*ports.ReleaseItem{{Title: "Fight.Club.1999.1080p.BluRay", GUID: "physical", SizeBytes: 1024, DownloadURL: "https://fixture.invalid/dl"}}, nil
}
func (*blockedEndpoint) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrUnavailable
}

type rateEndpoint struct{}

func (*rateEndpoint) Search(context.Context, string, []int32, int32, int32) ([]*ports.ReleaseItem, error) {
	return nil, ports.NewRateLimitError(time.Minute, "passkey=private-cookie")
}
func (*rateEndpoint) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrUnavailable
}

func TestPhysicalEndpointLeaseExpiryNeverUnlocksInFlightIO(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	slow := &blockedEndpoint{entered: make(chan struct{}), release: make(chan struct{})}
	f.svc.RegisterClient(f.idx, slow)
	round, err := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.queue.Claim(ctx, []string{"search_endpoint"}, "old-process", 150*time.Millisecond)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- f.svc.ExecuteEndpointJob(ctx, endpointTask(t, old)) }()
	<-slow.entered
	defer func() {
		select {
		case <-slow.release:
		default:
			close(slow.release)
		}
	}()
	// Prove a different endpoint still returns while this physical call is stalled.
	other, err := f.svc.CreateIndexer(ctx, "Other", "https://other.invalid", "key", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.RegisterClient(other.ID, &mockIndexerClient{})
	if _, err = f.svc.ExecuteSearch(ctx, other.ID, "other", 0); err != nil {
		t.Fatal("slow endpoint blocked independent endpoint", err)
	}
	time.Sleep(170 * time.Millisecond)
	if _, err = f.q.RecoverSearchJobs(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.queue.Claim(ctx, []string{"search_endpoint"}, "new-process", time.Minute)
	if err != nil || fresh == nil {
		t.Fatal(err)
	}
	restarted := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(make([]byte, 32)), 0)
	restarted.SetEndpointRepository(postgres.NewSearchEndpointStore(f.pool))
	restarted.RegisterClient(f.idx, &mockIndexerClient{})
	if release, e := postgres.AcquireIndexerPermit(ctx, f.pool, f.idx); e == nil {
		release()
		t.Fatal("logical expiry released physical exclusion")
	} else if !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	// Sharing the existing stable query is allowed, starting a second one is not.
	if err = restarted.ExecuteEndpointJob(ctx, endpointTask(t, fresh)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM indexer_query WHERE indexer_id=$1", f.idx).Scan(&count); err != nil || count != 1 || slow.calls.Load() != 1 {
		t.Fatal("physical request overlapped", count, err)
	}

	close(slow.release)
	if err = <-finished; err == nil {
		t.Fatal("stale physical caller wrote round evidence")
	}
	candidates, err := f.q.ListRoundQueryCandidates(ctx, round.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatal("fresh authority failed to share stable query result", candidates, err)
	}
	final := f.finalJob(t, round)
	if _, err = f.coord.FinalizeLeasedRound(ctx, round.ID, authority(final)); err != nil {
		t.Fatal(err)
	}
}
func TestOrphanedPhysicalExecutionIsRetiredBeforeRequery(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	idx, err := f.q.GetIndexerByID(ctx, f.idx)
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.q.CreateRunningQuery(ctx, db.CreateRunningQueryParams{ID: uuid.New(), IndexerID: f.idx, QueryType: "search", Keyword: "Fight Club 1999", ConfigVersion: idx.ConfigVersion})
	if err != nil {
		t.Fatal(err)
	}
	// No live physical permit exists: the old process died before its DB writeback.
	result, err := f.svc.ExecuteSearch(ctx, f.idx, "Fight Club 1999", 0)
	if err != nil || result.QueryID == old.ID || result.State != "succeeded" {
		t.Fatal("orphan was shared forever instead of observed and retired", result, err)
	}
	var state, note string
	err = f.pool.QueryRow(ctx, "SELECT state,error FROM indexer_query WHERE id=$1", old.ID).Scan(&state, &note)
	if err != nil || state != "failed" || note != "execution interrupted" {
		t.Fatal(state, note, err)
	}
}

func TestEndpointBudgetRateLimitPersistsAndNoSecretErrors(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	f.svc.RegisterClient(f.idx, &rateEndpoint{})
	if _, err := f.svc.ExecuteSearch(ctx, f.idx, "rate", 0); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	var delay bool
	var stored string
	if err := f.pool.QueryRow(ctx, "SELECT next_query_at>clock_timestamp()+interval '50 seconds' FROM indexer WHERE id=$1", f.idx).Scan(&delay); err != nil || !delay {
		t.Fatal("RetryAfter not persisted", delay, err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT error FROM indexer_query WHERE indexer_id=$1", f.idx).Scan(&stored); err != nil || stored != "indexer rate limited" {
		t.Fatal("unsafe error contract", stored, err)
	}
	f.svc.RegisterClient(f.idx, &mockIndexerClient{})
	if _, err := f.svc.ExecuteSearch(ctx, f.idx, "still-limited", 0); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("rate limit bypassed", err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE indexer SET next_query_at='-infinity' WHERE id=$1", f.idx); err != nil {
		t.Fatal(err)
	}
	production := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(make([]byte, 32)))
	production.RegisterClient(f.idx, &mockIndexerClient{})
	if _, err := production.ExecuteSearch(ctx, f.idx, "first", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := production.ExecuteSearch(ctx, f.idx, "second", 0); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("default 2s interval bypassed", err)
	}
}
func TestPersistentDeadlineUsesReturnedResultsDespitePendingEndpoint(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	slow, err := f.svc.CreateIndexer(ctx, "Pending", "https://pending.invalid", "key", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE subscription SET indexer_ids=jsonb_build_array($2::text,$3::text) WHERE id=$1", f.sub, f.idx.String(), slow.ID.String()); err != nil {
		t.Fatal(err)
	}
	round, err := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Claim precisely the healthy job, leave the second persisted endpoint pending.
	if _, err = f.pool.Exec(ctx, "UPDATE job SET priority=100 WHERE payload->>'indexer_id'=$1", f.idx.String()); err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(ctx, []string{"search_endpoint"}, "healthy", time.Minute)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = f.svc.ExecuteEndpointJob(ctx, endpointTask(t, job)); err != nil {
		t.Fatal(err)
	}
	if ready, err := f.q.IsSearchRoundReady(ctx, round.ID); err != nil || ready.Bool {
		t.Fatal("closed before deadline with pending endpoint", ready, err)
	}
	// Preserve completed-before-deadline evidence; model a persisted elapsed deadline.
	if _, err = f.pool.Exec(ctx, "UPDATE search_round SET deadline=clock_timestamp()+interval '50 milliseconds' WHERE id=$1", round.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	if ready, err := f.q.IsSearchRoundReady(ctx, round.ID); err != nil || !ready.Bool {
		t.Fatal(ready, err)
	}
	final := f.finalJob(t, round)
	rep, err := f.coord.FinalizeLeasedRound(ctx, round.ID, authority(final))
	if err != nil || rep.State != "completed" || rep.SelectedCandidateID == nil {
		t.Fatal(rep, err)
	}
	var pending int
	if err = f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_endpoint WHERE round_id=$1 AND state='pending'", round.ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatal(pending, err)
	}
	if _, err = f.coord.StartScheduledRound(ctx, f.wanted, time.Minute); err != nil {
		t.Fatal("completed round poisoned active unique dedup", err)
	}
}
