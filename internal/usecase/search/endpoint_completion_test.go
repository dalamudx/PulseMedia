package search_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"testing"
	"time"
)

func endpointScopeFixture(t *testing.T) (*roundFixture, *postgres.SearchEndpointStore, ports.SearchEndpointScope, uuid.UUID) {
	t.Helper()
	f := runtimeFixture(t)
	ctx := context.Background()
	if _, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute); e != nil {
		t.Fatal(e)
	}
	j, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "endpoint-owner", time.Minute)
	if e != nil || j == nil {
		t.Fatal(e)
	}
	repo := postgres.NewSearchEndpointStore(f.pool)
	scope, e := repo.Load(ctx, endpointTask(t, j))
	if e != nil {
		t.Fatal(e)
	}
	result, e := f.svc.ExecuteSearch(ctx, f.idx, "Fight Club 1999", 0)
	if e != nil {
		t.Fatal(e)
	}
	return f, repo, scope, result.QueryID
}
func endpointState(t *testing.T, f *roundFixture, scope ports.SearchEndpointScope) string {
	t.Helper()
	var state string
	if e := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object('job',to_jsonb(j),'endpoint',to_jsonb(e))::text FROM job j CROSS JOIN search_round_endpoint e WHERE j.id=$1 AND e.round_id=$2 AND e.indexer_id=$3`, scope.Task.Lease.ID, scope.Task.RoundID, scope.Task.IndexerID).Scan(&state); e != nil {
		t.Fatal(e)
	}
	return state
}
func TestEndpointCompletionFaultRollsBackAndReplays(t *testing.T) {
	for _, table := range []string{"search_round_endpoint", "job"} {
		t.Run(table, func(t *testing.T) {
			f, repo, scope, queryID := endpointScopeFixture(t)
			ctx := context.Background()
			before := endpointState(t, f, scope)
			if _, e := f.pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION endpoint_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected endpoint terminal fault'; END $$; CREATE TRIGGER endpoint_fault BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION endpoint_fault()`, table)); e != nil {
				t.Fatal(e)
			}
			if e := repo.Complete(ctx, scope, queryID); e == nil {
				t.Fatal("terminal fault succeeded")
			}
			if endpointState(t, f, scope) != before {
				t.Fatal("partial endpoint/job commit")
			}
			if _, e := f.pool.Exec(ctx, "DROP TRIGGER endpoint_fault ON "+table); e != nil {
				t.Fatal(e)
			}
			if e := repo.Complete(ctx, scope, queryID); e != nil {
				t.Fatal(e)
			}
			after := endpointState(t, f, scope)
			if e := repo.Complete(ctx, scope, queryID); e == nil {
				t.Fatal("completed lease accepted")
			}
			if endpointState(t, f, scope) != after {
				t.Fatal("old replay changed receipt")
			}
			var state, endpoint string
			var linked uuid.UUID
			if e := f.pool.QueryRow(ctx, `SELECT j.state,e.state,e.query_id FROM job j CROSS JOIN search_round_endpoint e WHERE j.id=$1 AND e.round_id=$2 AND e.indexer_id=$3`, scope.Task.Lease.ID, scope.Task.RoundID, scope.Task.IndexerID).Scan(&state, &endpoint, &linked); e != nil || state != "succeeded" || endpoint != "returned" || linked != queryID {
				t.Fatal(state, endpoint, e)
			}
		})
	}
}
func TestEndpointCompletionRejectsWrongQueryAndClosedSources(t *testing.T) {
	for _, kind := range []string{"keyword", "page", "foreign", "closed", "config", "health"} {
		t.Run(kind, func(t *testing.T) {
			f, repo, scope, queryID := endpointScopeFixture(t)
			ctx := context.Background()
			if kind == "keyword" || kind == "page" || kind == "foreign" {
				idx := f.idx
				word := "Fight Club 1999"
				page := int32(0)
				switch kind {
				case "keyword":
					word = "wrong work"
				case "page":
					page = 1
				case "foreign":
					other, e := f.svc.CreateIndexer(ctx, "foreign", "https://scope.invalid", "synthetic", nil)
					if e != nil {
						t.Fatal(e)
					}
					idx = other.ID
					f.svc.RegisterClient(idx, &endpointScopeClient{})
				}
				result, e := f.svc.ExecuteSearch(ctx, idx, word, page)
				if e != nil {
					t.Fatal(e)
				}
				queryID = result.QueryID
				before := endpointState(t, f, scope)
				if e = repo.Complete(ctx, scope, queryID); e == nil {
					t.Fatal("wrong query scope accepted")
				}
				if endpointState(t, f, scope) != before {
					t.Fatal("wrong query changed task")
				}
				return
			}
			statement := "UPDATE search_round SET state='aborted' WHERE id=$1"
			id := scope.Task.RoundID
			if kind == "config" {
				statement = "UPDATE indexer SET config_version=config_version+1 WHERE id=$1"
				id = f.idx
			}
			if kind == "health" {
				statement = "UPDATE indexer SET health='offline' WHERE id=$1"
				id = f.idx
			}
			if _, e := f.pool.Exec(ctx, statement, id); e != nil {
				t.Fatal(e)
			}
			if e := repo.Complete(ctx, scope, queryID); e != nil {
				t.Fatal(e)
			}
			var state string
			var linked bool
			if e := f.pool.QueryRow(ctx, `SELECT state,query_id IS NOT NULL FROM search_round_endpoint WHERE round_id=$1 AND indexer_id=$2`, scope.Task.RoundID, f.idx).Scan(&state, &linked); e != nil || state != "failed" || linked {
				t.Fatal(state, linked, e)
			}
		})
	}
}

type endpointSwapRepository struct {
	ports.SearchEndpointRepository
	swap func()
}

func (r endpointSwapRepository) Load(ctx context.Context, task ports.SearchEndpointTask) (ports.SearchEndpointScope, error) {
	scope, e := r.SearchEndpointRepository.Load(ctx, task)
	if e == nil {
		r.swap()
	}
	return scope, e
}
func TestEndpointUsesFrozenConfigAfterScopeLoad(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	if _, e := f.coord.StartScheduledRound(ctx, f.wanted, time.Minute); e != nil {
		t.Fatal(e)
	}
	job, e := f.queue.Claim(ctx, []string{"search_endpoint"}, "scope-owner", time.Minute)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	c := &endpointScopeClient{}
	f.svc.RegisterClient(f.idx, c)
	f.svc.SetEndpointRepository(endpointSwapRepository{SearchEndpointRepository: postgres.NewSearchEndpointStore(f.pool), swap: func() {
		if _, e := f.pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", f.idx); e != nil {
			t.Fatal(e)
		}
	}})
	if e = f.svc.ExecuteEndpointJob(ctx, endpointTask(t, job)); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	var queries int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM indexer_query").Scan(&queries); e != nil || queries != 0 || c.calls.Load() != 0 {
		t.Fatal("new config silently adopted", queries, c.calls.Load(), e)
	}
	f.svc.SetEndpointRepository(postgres.NewSearchEndpointStore(f.pool))
	if e = f.svc.ExecuteEndpointJob(ctx, endpointTask(t, job)); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = f.pool.QueryRow(ctx, `SELECT state FROM search_round_endpoint WHERE round_id=(SELECT (payload->>'round_id')::uuid FROM job WHERE id=$1) AND indexer_id=$2`, job.ID, f.idx).Scan(&state); e != nil || state != "failed" {
		t.Fatal(state, e)
	}
}
