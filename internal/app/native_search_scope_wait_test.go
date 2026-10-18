package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func waitSearchScopeLock(t *testing.T, f movieLinkFixture, blocker uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if e := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1::int=ANY(pg_blocking_pids(pid)))`, int64(blocker)).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("search did not reach endpoint lock")
		}
	}
}

func TestNativeSearchScopeRechecksAfterBudgetAndCommitWait(t *testing.T) {
	for _, stage := range []string{"budget", "commit"} {
		for _, mutation := range []string{"config_version=config_version+1", "health='offline'"} {
			t.Run(stage+"/"+mutation, func(t *testing.T) {
				f := newMovieLinkFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				entered, release := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 && stage == "commit" {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					_, _ = fmt.Fprint(w, `<rss><channel><response offset="0" total="1"/><item><title>Fight.Club.1999.1080p.BluRay</title><guid>locked-source</guid><link>https://fixture.invalid/?passkey=synthetic</link></item></channel></rss>`)
				}))
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
					cancel()
					stub.Close()
				}()
				svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 0)
				idx, e := svc.CreateIndexer(ctx, "scope lock", stub.URL, "synthetic", nil)
				if e != nil {
					t.Fatal(e)
				}
				svc.RegisterClient(idx.ID, torznab.NewClient(stub.URL, "synthetic", stub.Client()))
				var before string
				if e = f.pool.QueryRow(ctx, "SELECT next_query_at::text FROM indexer WHERE id=$1", idx.ID).Scan(&before); e != nil {
					t.Fatal(e)
				}
				tx, e := f.pool.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				lock := func() {
					if _, err := tx.Exec(ctx, "SELECT id FROM indexer WHERE id=$1 FOR UPDATE", idx.ID); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "budget" {
					lock()
				}
				done := make(chan error, 1)
				go func() { _, err := svc.ExecuteSearch(ctx, idx.ID, "Fight Club", 0); done <- err }()
				if stage == "commit" {
					select {
					case <-entered:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					lock()
					close(release)
				}
				waitSearchScopeLock(t, f, tx.Conn().PgConn().PID())
				if _, e = tx.Exec(ctx, "UPDATE indexer SET "+mutation+" WHERE id=$1", idx.ID); e != nil {
					t.Fatal(e)
				}
				if e = tx.Commit(ctx); e != nil {
					t.Fatal(e)
				}
				select {
				case e = <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if !errors.Is(e, ports.ErrUnavailable) {
					t.Fatal("stale source accepted", e)
				}
				var candidates, proofs, queries int
				var after string
				if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM release_candidate),(SELECT count(*) FROM indexer_query_page),(SELECT count(*) FROM indexer_query),(SELECT next_query_at::text FROM indexer WHERE id=$1)`, idx.ID).Scan(&candidates, &proofs, &queries, &after); e != nil || candidates != 0 || proofs != 0 {
					t.Fatal(candidates, proofs, queries, e)
				}
				if stage == "budget" && (calls.Load() != 0 || queries != 0 || after != before) {
					t.Fatal("stale wait advanced budget, query or HTTP")
				}
				if stage == "commit" && (calls.Load() != 1 || queries != 1) {
					t.Fatal("commit stage missing", calls.Load(), queries)
				}
				if _, e = f.pool.Exec(ctx, "UPDATE indexer SET health='online' WHERE id=$1", idx.ID); e != nil {
					t.Fatal(e)
				}
				if _, e = svc.ExecuteSearch(ctx, idx.ID, "Fight Club", 0); e != nil {
					t.Fatal("physical slot did not recover", e)
				}
			})
		}
	}
}

func TestNativeAggregateRechecksEarlierAndNotYetQueriedEndpoints(t *testing.T) {
	for _, stage := range []string{"earlier", "not_yet"} {
		t.Run(stage, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			entered, release := make(chan struct{}, 4), make(chan struct{})
			var firstCalls, secondCalls atomic.Int32
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				first, second := r.URL.Path == "/first/api", r.URL.Path == "/second/api"
				var n int32
				if first {
					n = firstCalls.Add(1)
				} else if second {
					n = secondCalls.Add(1)
				}
				hold := (stage == "earlier" && second && n == 1) || (stage == "not_yet" && !second)
				if hold {
					select {
					case <-release:
					default:
						select {
						case entered <- struct{}{}:
						case <-r.Context().Done():
							return
						}
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
				}
				_, _ = fmt.Fprintf(w, `<rss><channel><response offset="0" total="1"/><item><title>Fight.Club.1999.1080p.BluRay</title><guid>%s</guid><link>https://fixture.invalid/?passkey=synthetic</link></item></channel></rss>`, r.URL.Path)
			}))
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				cancel()
				stub.Close()
			}()
			svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 0)
			a, e := svc.CreateIndexer(ctx, "first", stub.URL+"/first", "synthetic", nil)
			if e != nil {
				t.Fatal(e)
			}
			svc.RegisterClient(a.ID, torznab.NewClient(a.Url, "synthetic", stub.Client()))
			expectedIDs := make(map[string]bool)
			barrierCount := 1
			if stage == "not_yet" {
				// Hold all four workers so the changed fifth source is still queued.
				barrierCount = 4
				expectedIDs[a.ID.String()] = true
				for i := range 3 {
					path := fmt.Sprintf("/held-%d", i)
					idx, err := svc.CreateIndexer(ctx, fmt.Sprintf("held-%d", i), stub.URL+path, "synthetic", nil)
					if err != nil {
						t.Fatal(err)
					}
					svc.RegisterClient(idx.ID, torznab.NewClient(idx.Url, "synthetic", stub.Client()))
					expectedIDs[idx.ID.String()] = true
				}
			}
			b, e := svc.CreateIndexer(ctx, "second", stub.URL+"/second", "synthetic", nil)
			if e != nil {
				t.Fatal(e)
			}
			svc.RegisterClient(b.ID, torznab.NewClient(b.Url, "synthetic", stub.Client()))
			mutate := a.ID
			if stage == "not_yet" {
				mutate = b.ID
			} else {
				expectedIDs[b.ID.String()] = true
			}
			type result struct {
				count   int
				partial bool
				err     error
				correct bool
			}
			done := make(chan result, 1)
			go func() {
				rows, partial, err := svc.AggregateSearchDetailed(ctx, "Fight Club")
				correct := len(rows) == len(expectedIDs)
				for _, row := range rows {
					correct = correct && expectedIDs[row.IndexerID.String()]
				}
				done <- result{len(rows), partial, err, correct}
			}()
			for range barrierCount {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if stage == "earlier" {
				// The earlier result must already be committed before its version changes.
				for {
					var committed bool
					if e := f.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM indexer_query WHERE indexer_id=$1 AND state='succeeded')", a.ID).Scan(&committed); e != nil {
						t.Fatal(e)
					}
					if committed {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(5 * time.Millisecond):
					}
				}
			}
			if _, e = f.pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", mutate); e != nil {
				t.Fatal(e)
			}
			close(release)
			var got result
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if got.err != nil || !got.partial || !got.correct {
				t.Fatal("mixed configuration results", got)
			}
			if stage == "not_yet" && secondCalls.Load() != 0 {
				t.Fatal("sent new config into old aggregate")
			}
			rows, partial, e := svc.AggregateSearchDetailed(ctx, "Fight Club")
			if e != nil || partial || len(rows) != len(expectedIDs)+1 {
				t.Fatal("fresh aggregate did not recover", len(rows), partial, e)
			}
		})
	}
}
