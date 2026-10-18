package app_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pageProofItem = `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>proof-item</guid><link>https://fixture.invalid/?passkey=synthetic-proof</link></item>`

func pageEvidenceFixture(t *testing.T) (*search.Service, *pgxpool.Pool, uuid.UUID, *atomic.Value) {
	t.Helper()
	f := newMovieLinkFixture(t)
	body := &atomic.Value{}
	body.Store(`<rss><channel><response offset="0" total="1"/>` + pageProofItem + `</channel></rss>`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body.Load().(string)) }))
	t.Cleanup(server.Close)
	svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 0)
	idx, e := svc.CreateIndexer(context.Background(), "page proof fixture", server.URL, "synthetic-proof-key", nil)
	if e != nil {
		t.Fatal(e)
	}
	svc.RegisterClient(idx.ID, torznab.NewClient(server.URL, "synthetic-proof-key", server.Client()))
	return svc, f.pool, idx.ID, body
}
func TestIndexerPageEvidenceFailureRollbackAndRetry(t *testing.T) {
	for _, table := range []string{"indexer_query_page", "indexer_query"} {
		t.Run(table, func(t *testing.T) {
			svc, pool, id, _ := pageEvidenceFixture(t)
			ctx := context.Background()
			operation := "INSERT"
			when := ""
			if table == "indexer_query" {
				operation = "UPDATE"
				when = " WHEN (NEW.state='succeeded')"
			}
			_, e := pool.Exec(ctx, `CREATE FUNCTION page_proof_fault() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'page proof fault';END$$`)
			if e != nil {
				t.Fatal(e)
			}
			_, e = pool.Exec(ctx, fmt.Sprintf("CREATE TRIGGER page_proof_fault BEFORE %s ON %s FOR EACH ROW%s EXECUTE FUNCTION page_proof_fault()", operation, table, when))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = svc.ExecuteSearch(ctx, id, "Fight Club", 0); e == nil {
				t.Fatal("fault acknowledged success")
			}
			var proofs, candidates, links, success int
			if e = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM indexer_query_page),(SELECT count(*) FROM release_candidate),(SELECT count(*) FROM indexer_query_candidate),(SELECT count(*) FROM indexer_query WHERE state='succeeded')`).Scan(&proofs, &candidates, &links, &success); e != nil || proofs+candidates+links+success != 0 {
				t.Fatal("partial commit", proofs, candidates, links, success, e)
			}
			if _, e = pool.Exec(ctx, "DROP TRIGGER page_proof_fault ON "+table); e != nil {
				t.Fatal(e)
			}
			rows, partial, e := svc.AggregateSearchDetailed(ctx, "Fight Club")
			if e != nil || partial || len(rows) != 1 {
				t.Fatal("retry", len(rows), partial, e)
			}
		})
	}
}
func TestIndexerPageEvidenceDuplicateDoesNotInflateCompleteness(t *testing.T) {
	svc, pool, id, body := pageEvidenceFixture(t)
	ctx := context.Background()
	body.Store(`<rss><channel><response offset="0" total="2"/>` + pageProofItem + pageProofItem + `</channel></rss>`)
	if _, e := svc.ExecuteSearch(ctx, id, "duplicates", 0); e == nil {
		t.Fatal("duplicate identity certified total")
	}
	var n int
	if e := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM indexer_query_page)+(SELECT count(*) FROM release_candidate)+(SELECT count(*) FROM indexer_query_candidate)`).Scan(&n); e != nil || n != 0 {
		t.Fatal("duplicate transaction persisted", n, e)
	}
}
func TestIndexerPageEvidenceHistoryAndMigration(t *testing.T) {
	svc, pool, id, _ := pageEvidenceFixture(t)
	ctx := context.Background()
	dir := filepath.Join("..", "..", "migrations")
	for version := int64(postgres.CurrentMigrationVersion); version > 52; {
		next, e := postgres.Rollback(ctx, pool.Config().ConnConfig.ConnString(), dir)
		if e != nil || next != version-1 {
			t.Fatal(next, e)
		}
		version = next
	}
	old := uuid.New()
	if _, e := pool.Exec(ctx, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,created_at,finished_at) VALUES($1,$2,'search','legacy',0,1,'succeeded',0,clock_timestamp(),clock_timestamp())`, old, id); e != nil {
		t.Fatal(e)
	}
	var before string
	if e := pool.QueryRow(ctx, `SELECT to_jsonb(q)::text FROM indexer_query q WHERE id=$1`, old).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(ctx, pool.Config().ConnConfig.ConnString(), dir); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, e)
	}
	q := postgres.Queries(pool)
	if complete, e := q.IsIndexerQueryComplete(ctx, old); e != nil || complete {
		t.Fatal("legacy proof fabricated", complete, e)
	}
	var after string
	if e := pool.QueryRow(ctx, `SELECT to_jsonb(q)::text FROM indexer_query q WHERE id=$1`, old).Scan(&after); e != nil || after != before {
		t.Fatal("legacy changed", e)
	}
	result, e := svc.ExecuteSearch(ctx, id, "Fight Club", 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, sql := range []string{`UPDATE indexer_query_page SET total=2 WHERE query_id=$1`, `DELETE FROM indexer_query_page WHERE query_id=$1`, `UPDATE indexer_query SET page=1 WHERE id=$1`, `UPDATE indexer_query SET config_version=2 WHERE id=$1`, `UPDATE indexer_query SET state='failed' WHERE id=$1`, `DELETE FROM indexer_query_candidate WHERE query_id=$1`, `INSERT INTO indexer_query_candidate SELECT query_id,candidate_id FROM indexer_query_candidate WHERE query_id=$1 ON CONFLICT DO NOTHING`} {
		if _, e = pool.Exec(ctx, sql, result.QueryID); e == nil {
			t.Fatal("history mutation accepted", sql)
		}
	}
	for version := int64(postgres.CurrentMigrationVersion); version > 53; {
		next, e := postgres.Rollback(ctx, pool.Config().ConnConfig.ConnString(), dir)
		if e != nil || next != version-1 {
			t.Fatal(next, e)
		}
		version = next
	}
	if _, e = postgres.Rollback(ctx, pool.Config().ConnConfig.ConnString(), dir); e == nil {
		t.Fatal("history removed by downgrade")
	}
	if complete, e := q.IsIndexerQueryComplete(ctx, result.QueryID); e != nil || !complete {
		t.Fatal("proven page lost", complete, e)
	}
}
