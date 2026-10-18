package search_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

type persistentPageClient struct {
	mu      sync.Mutex
	offsets []int32
	mode    string
}

func (c *persistentPageClient) Search(ctx context.Context, k string, cats []int32, o, l int32) ([]*ports.ReleaseItem, error) {
	p, e := c.SearchPage(ctx, k, cats, o, l)
	if e != nil {
		return nil, e
	}
	return p.Items, nil
}
func (c *persistentPageClient) FetchTorrent(context.Context, string) ([]byte, error) {
	return nil, ports.ErrRejected
}
func (c *persistentPageClient) SearchPage(_ context.Context, _ string, _ []int32, offset, limit int32) (*ports.IndexerPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offsets = append(c.offsets, offset)
	total := int64(51)
	count, start := 50, int(offset)
	if offset > 0 {
		count = 1
		if c.mode == "duplicate" {
			start = 0
		}
		if c.mode == "changed total" {
			total = 52
		}
	}
	items := make([]*ports.ReleaseItem, 0, count)
	for n := 0; n < count; n++ {
		items = append(items, &ports.ReleaseItem{GUID: fmt.Sprintf("persistent-%d", start+n), Title: fmt.Sprintf("Fight.Club.1999.1080p.BluRay.x264-GROUP%d", start+n), DownloadURL: fmt.Sprintf("https://tracker.example/download/%d?secret=synthetic", start+n), SizeBytes: 1024, Seeders: 20})
	}
	return &ports.IndexerPage{Items: items, Offset: offset, Limit: limit, OffsetVerified: true, Total: &total}, nil
}
func freshPageService(f *roundFixture, c *persistentPageClient) *usecaseSearch.Service {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s := usecaseSearch.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(key), time.Millisecond)
	s.SetEndpointRepository(postgres.NewSearchEndpointStore(f.pool))
	s.RegisterClient(f.idx, c)
	return s
}
func TestPersistentEndpointPagesReconstructCursor(t *testing.T) {
	for _, mode := range []string{"success", "duplicate", "changed total"} {
		t.Run(mode, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx := context.Background()
			client := &persistentPageClient{mode: mode}
			f.svc = freshPageService(f, client)
			round, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			job, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "first-page-owner", time.Minute)
			if e != nil || job == nil {
				t.Fatal(e)
			}
			task := endpointTask(t, job)
			if e = f.svc.ExecuteEndpointJob(ctx, task); e != nil {
				t.Fatal(e)
			}
			var jobState, endpointState string
			var saved int
			if e = f.pool.QueryRow(ctx, "SELECT j.state,e.state,(SELECT count(*) FROM search_round_endpoint_page) FROM job j JOIN search_round_endpoint e ON e.round_id=$2 AND e.indexer_id=$3 WHERE j.id=$1", job.ID, round.ID, f.idx).Scan(&jobState, &endpointState, &saved); e != nil || jobState != "queued" || endpointState != "pending" || saved != 1 {
				t.Fatal(jobState, endpointState, saved, e)
			}
			ready, e := f.q.ListReadySearchRounds(ctx, 10)
			if e != nil || len(ready) != 0 {
				t.Fatal("premature finalization", ready, e)
			}
			// A newly constructed service has no in-memory continuation state.
			rebuilt := freshPageService(f, client)
			second, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "restart-page-owner", time.Minute)
			if e != nil || second == nil {
				t.Fatal(e)
			}
			if second.ID != job.ID || second.LeaseToken <= job.LeaseToken {
				t.Fatal("did not resume same task with new token")
			}
			scope, e := postgres.NewSearchEndpointStore(f.pool).Load(ctx, endpointTask(t, second))
			if e != nil || scope.NextPage != 1 || len(scope.SavedQueries) != 1 || len(scope.SeenGUIDs) != 50 {
				t.Fatal(scope, e)
			}
			if e = rebuilt.ExecuteEndpointJob(ctx, task); e == nil {
				t.Fatal("old owner accepted")
			}
			e = rebuilt.ExecuteEndpointJob(ctx, endpointTask(t, second))
			if mode == "success" {
				if e != nil {
					t.Fatal(e)
				}
			} else {
				if !errors.Is(e, ports.ErrUnavailable) {
					t.Fatal("invalid page did not retain retry contract", e)
				}
				var pages int
				if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_endpoint_page").Scan(&pages); e != nil || pages != 1 {
					t.Fatal(pages, e)
				}
				if _, e = f.pool.Exec(ctx, "UPDATE search_round SET deadline=clock_timestamp()+interval '60 milliseconds' WHERE id=$1", round.ID); e != nil {
					t.Fatal(e)
				}
				time.Sleep(80 * time.Millisecond)
				if e = rebuilt.ExecuteEndpointJob(ctx, endpointTask(t, second)); e != nil {
					t.Fatal("deadline did not safely close saved pages", e)
				}
			}
			rows, e := f.q.ListRoundQueryCandidates(ctx, round.ID)
			if e != nil {
				t.Fatal(e)
			}
			want := 50
			wantPages := 1
			if mode == "success" {
				want = 51
				wantPages = 2
			}
			if len(rows) != want {
				t.Fatal("lost prior page or admitted invalid page", len(rows), want)
			}
			if e = f.pool.QueryRow(ctx, "SELECT j.state,e.state,(SELECT count(*) FROM search_round_endpoint_page) FROM job j JOIN search_round_endpoint e ON e.round_id=$2 AND e.indexer_id=$3 WHERE j.id=$1", job.ID, round.ID, f.idx).Scan(&jobState, &endpointState, &saved); e != nil || jobState != "succeeded" || endpointState != "returned" || saved != wantPages {
				t.Fatal(jobState, endpointState, saved, e)
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			if len(client.offsets) != 2 || client.offsets[0] != 0 || client.offsets[1] != 50 {
				t.Fatal("replayed first page", client.offsets)
			}
		})
	}
}
func TestPersistentEndpointPageAndYieldRollback(t *testing.T) {
	for _, table := range []string{"search_round_endpoint_page", "job"} {
		t.Run(table, func(t *testing.T) {
			f := runtimeFixture(t)
			ctx := context.Background()
			client := &persistentPageClient{}
			f.svc = freshPageService(f, client)
			_, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute)
			if e != nil {
				t.Fatal(e)
			}
			job, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "page-fault-owner", time.Minute)
			if e != nil || job == nil {
				t.Fatal(e)
			}
			repo := postgres.NewSearchEndpointStore(f.pool)
			scope, e := repo.Load(ctx, endpointTask(t, job))
			if e != nil {
				t.Fatal(e)
			}
			result, e := f.svc.ExecuteSearch(ctx, f.idx, "Fight Club 1999", 0)
			if e != nil {
				t.Fatal(e)
			}
			before := endpointState(t, f, scope)
			op := "UPDATE"
			if table == "search_round_endpoint_page" {
				op = "INSERT"
			}
			if _, e = f.pool.Exec(ctx, fmt.Sprintf("CREATE FUNCTION page_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected page fault'; END $$;CREATE TRIGGER page_fault BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION page_fault()", op, table)); e != nil {
				t.Fatal(e)
			}
			if e = repo.Complete(ctx, scope, result.QueryID); e == nil {
				t.Fatal("fault accepted")
			}
			var saved int
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM search_round_endpoint_page").Scan(&saved); e != nil || saved != 0 || endpointState(t, f, scope) != before {
				t.Fatal("partial page/yield", saved, e)
			}
			if _, e = f.pool.Exec(ctx, "DROP TRIGGER page_fault ON "+table); e != nil {
				t.Fatal(e)
			}
			if e = repo.Complete(ctx, scope, result.QueryID); e != nil {
				t.Fatal(e)
			}
			if e = repo.Complete(ctx, scope, result.QueryID); e == nil {
				t.Fatal("stale owner replay accepted")
			}
			var links int
			if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM indexer_query_candidate WHERE query_id=$1", result.QueryID).Scan(&links); e != nil || links != 50 {
				t.Fatal(links, e)
			}
			if rows, e := f.q.ListSearchEndpointPages(ctx, db.ListSearchEndpointPagesParams{RoundID: scope.Task.RoundID, IndexerID: f.idx}); e != nil || len(rows) != 1 {
				t.Fatal(rows, e)
			}
		})
	}
}
