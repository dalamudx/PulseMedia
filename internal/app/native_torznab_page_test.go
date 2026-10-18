package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestNativeTorznabPageFailurePreservesCandidatesAndAllowsRetry(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var body atomic.Value
	var calls atomic.Int32
	var lastOffset atomic.Int64
	item := func(guid string) string {
		return `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>` + guid + `</guid><enclosure url="https://example.invalid/get?passkey=synthetic-page-link" length="1000"/></item>`
	}
	body.Store(`<rss><channel><response offset="0" total="51"/>` + item("original-page") + `</channel></rss>`)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		lastOffset.Store(offset)
		_, _ = fmt.Fprint(w, body.Load().(string))
	}))
	defer stub.Close()
	ids := idgen.New()
	key := bytes.Repeat([]byte{7}, 32)
	secret, e := crypto.Encrypt(key, []byte("synthetic-page-key"))
	if e != nil {
		t.Fatal(e)
	}
	q := postgres.Queries(f.pool)
	indexer, e := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: ids.NewUUID(), Name: "native page contract", Url: stub.URL, ApiKeyEncrypted: secret, Categories: []byte("[]")})
	if e != nil {
		t.Fatal(e)
	}
	svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), ids, crypto.NewSecretProtector(key), 0)
	svc.RegisterClient(indexer.ID, torznab.NewClient(stub.URL, "synthetic-page-key", nil))
	first, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", 0)
	if e != nil || first.ResultsCount != 1 {
		t.Fatal(first, e)
	}
	snapshot := func() string {
		var value string
		if e := f.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]'::jsonb)::text FROM release_candidate c WHERE indexer_id=$1`, indexer.ID).Scan(&value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	before := snapshot()
	var budget time.Time
	if e = f.pool.QueryRow(ctx, `SELECT next_query_at FROM indexer WHERE id=$1`, indexer.ID).Scan(&budget); e != nil {
		t.Fatal(e)
	}
	for _, page := range []int32{-1, math.MaxInt32/50 + 1, math.MaxInt32} {
		if result, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", page); !errors.Is(e, ports.ErrBadResponse) || result != nil {
			t.Fatalf("invalid page accepted: %v %v", result, e)
		}
	}
	var count int
	var afterBudget time.Time
	if e = f.pool.QueryRow(ctx, `SELECT next_query_at,(SELECT count(*) FROM indexer_query WHERE indexer_id=$1) FROM indexer WHERE id=$1`, indexer.ID).Scan(&afterBudget, &count); e != nil || count != 1 || !afterBudget.Equal(budget) || calls.Load() != 1 {
		t.Fatal("invalid page had side effects", count, calls.Load(), e)
	}
	for _, bad := range []string{
		`<rss><channel><response offset="0" total="51"/>` + item("original-page") + `</channel></rss>`,
		`<rss><channel>` + item("unproven-page") + `</channel></rss>`,
		`<rss><channel><response offset="50" total="50"/>` + item("contradictory-page") + `</channel></rss>`,
		`<rss><channel/><channel>` + item("extra-channel") + `</channel></rss>`,
	} {
		body.Store(bad)
		result, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", 1)
		if !errors.Is(e, ports.ErrUnavailable) || result == nil || result.State != "failed" || lastOffset.Load() != 50 {
			t.Fatal(result, e, lastOffset.Load())
		}
		var state, message string
		var links int
		if e = f.pool.QueryRow(ctx, `SELECT state,error,(SELECT count(*) FROM indexer_query_candidate WHERE query_id=$1) FROM indexer_query WHERE id=$1`, result.QueryID).Scan(&state, &message, &links); e != nil || state != "failed" || message != "indexer search failed" || links != 0 {
			t.Fatal(state, message, links, e)
		}
		if snapshot() != before {
			t.Fatal("failed page mutated prior evidence")
		}
	}
	body.Store(`<rss><channel><response offset="50" total="51"/>` + item("next-page") + `</channel></rss>`)
	second, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", 1)
	if e != nil || second.ResultsCount != 1 || second.FromShared {
		t.Fatal(second, e)
	}
	after := snapshot()
	retry, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", 1)
	if e != nil || retry.QueryID == second.QueryID || retry.ResultsCount != 1 || retry.FromShared || snapshot() != after {
		t.Fatal(retry, e, "same-page retry changed candidate evidence")
	}
	body.Store(`<rss><channel><response offset="100" total="51"/></channel></rss>`)
	end, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", 2)
	if e != nil || end.ResultsCount != 0 || snapshot() != after || lastOffset.Load() != 100 {
		t.Fatal(end, e)
	}
	body.Store(`<rss><channel><response offset="2147483600" total="0"/></channel></rss>`)
	bound, e := svc.ExecuteSearch(ctx, indexer.ID, "Fight Club", math.MaxInt32/50)
	if e != nil || bound.ResultsCount != 0 || lastOffset.Load() != 2147483600 {
		t.Fatal(bound, e, lastOffset.Load())
	}
}
