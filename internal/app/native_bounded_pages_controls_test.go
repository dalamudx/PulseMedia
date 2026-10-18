package app_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestNativeAggregateContinuationBudgetAndSourceControls(t *testing.T) {
	for _, mode := range []string{"wait succeeds", "configuration changes", "cooling too long"} {
		t.Run(mode, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx := context.Background()
			var calls atomic.Int32
			var due time.Time
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				offset := r.URL.Query().Get("offset")
				start, count := 0, 50
				if offset == "50" {
					start, count = 50, 1
					var now time.Time
					if e := f.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); e != nil || now.Before(due) {
						t.Error("second request started before budget", now, due, e)
					}
				} else {
					if e := f.pool.QueryRow(ctx, "SELECT next_query_at FROM indexer WHERE name=$1", mode).Scan(&due); e != nil {
						t.Error(e)
					}
					if mode == "configuration changes" {
						if _, e := f.pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE name=$1", mode); e != nil {
							t.Error(e)
						}
					}
					if mode == "cooling too long" {
						if _, e := f.pool.Exec(ctx, "UPDATE indexer SET next_query_at=clock_timestamp()+interval '1 hour' WHERE name=$1", mode); e != nil {
							t.Error(e)
						}
					}
				}
				_, e := fmt.Fprintf(w, `<rss><channel><response offset="%d" total="51"/>%s</channel></rss>`, start, boundedPageItems(start, count))
				if e != nil {
					t.Error(e)
				}
			}))
			defer stub.Close()
			svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 350*time.Millisecond)
			idx, e := svc.CreateIndexer(ctx, mode, stub.URL, "synthetic-key", nil)
			if e != nil {
				t.Fatal(e)
			}
			svc.RegisterClient(idx.ID, torznab.NewClient(stub.URL, "synthetic-key", stub.Client()))
			rows, partial, e := svc.AggregateSearchDetailed(ctx, "Fight Club")
			var queryCount int
			if dbErr := f.pool.QueryRow(ctx, "SELECT count(*) FROM indexer_query").Scan(&queryCount); dbErr != nil {
				t.Fatal(dbErr)
			}
			switch mode {
			case "wait succeeds":
				if e != nil || !partial || len(rows) != 51 || calls.Load() != 2 || queryCount != 2 {
					t.Fatal(len(rows), partial, calls.Load(), queryCount, e)
				}
			case "configuration changes":
				if e == nil || len(rows) != 0 || calls.Load() != 1 || queryCount != 1 {
					t.Fatal(len(rows), calls.Load(), queryCount, e)
				}
			case "cooling too long":
				if e != nil || !partial || len(rows) != 50 || calls.Load() != 1 || queryCount != 1 {
					t.Fatal(len(rows), partial, calls.Load(), queryCount, e)
				}
			}
			if e != nil && strings.Contains(e.Error(), "synthetic-key") {
				t.Fatal("unsafe error")
			}
		})
	}
}
