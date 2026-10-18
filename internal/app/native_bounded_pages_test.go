package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/jackc/pgx/v5"
)

func boundedPageItems(start, count int) string {
	var b strings.Builder
	for n := start; n < start+count; n++ {
		fmt.Fprintf(&b, `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>bounded-%d</guid><link>https://fixture.invalid/%d?passkey=synthetic-bounded</link></item>`, n, n)
	}
	return b.String()
}
func TestNativeAggregateBoundedPagesPreservePartialEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		total, rows, calls, failed int
	}{
		{"exhausted", 51, 51, 2, 0}, {"cap", 500, 200, 4, 0},
		{"total changed", 51, 50, 2, 1}, {"duplicate", 51, 50, 2, 1}, {"unknown last", 51, 50, 2, 1},
		{"rate limited", 51, 50, 2, 1}, {"unavailable", 51, 50, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMovieLinkFixture(t)
			ctx := context.Background()
			var calls atomic.Int32
			var firstCipher []byte
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				offsetText := r.URL.Query().Get("offset")
				if offsetText == "" {
					offsetText = "0"
				}
				offset, e := strconv.Atoi(offsetText)
				if e != nil {
					t.Error(e)
					w.WriteHeader(500)
					return
				}
				total := tc.total
				start, count := offset, 50
				if total-offset < count {
					count = total - offset
				}
				if offset > 0 {
					if e := f.pool.QueryRow(ctx, "SELECT link_encrypted FROM release_candidate WHERE guid='bounded-0'").Scan(&firstCipher); e != nil {
						t.Error(e)
					}
					switch tc.name {
					case "total changed":
						total++
					case "duplicate":
						start = 0
					case "unknown last":
						_, e = fmt.Fprint(w, `<rss><channel>`+boundedPageItems(start, count)+`</channel></rss>`)
						if e != nil {
							t.Error(e)
						}
						return
					case "rate limited":
						w.Header().Set("Retry-After", "30")
						w.WriteHeader(429)
						return
					case "unavailable":
						w.WriteHeader(503)
						return
					}
				}
				_, e = fmt.Fprintf(w, `<rss><channel><response offset="%d" total="%d"/>%s</channel></rss>`, offset, total, boundedPageItems(start, count))
				if e != nil {
					t.Error(e)
				}
			}))
			defer stub.Close()
			svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 0)
			idx, e := svc.CreateIndexer(ctx, "bounded "+tc.name, stub.URL, "synthetic-key", nil)
			if e != nil {
				t.Fatal(e)
			}
			svc.RegisterClient(idx.ID, torznab.NewClient(stub.URL, "synthetic-key", stub.Client()))
			rows, partial, e := svc.AggregateSearchDetailed(ctx, "Fight Club")
			if e != nil || !partial || len(rows) != tc.rows || int(calls.Load()) != tc.calls {
				t.Fatalf("rows=%d partial=%v calls=%d err=%v", len(rows), partial, calls.Load(), e)
			}
			var candidateCount, proofCount, failedCount int
			if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM release_candidate),(SELECT count(*) FROM indexer_query_page),(SELECT count(*) FROM indexer_query WHERE state='failed')").Scan(&candidateCount, &proofCount, &failedCount); e != nil || candidateCount != tc.rows || proofCount != tc.calls-tc.failed || failedCount != tc.failed {
				t.Fatal(candidateCount, proofCount, failedCount, e)
			}
			if tc.failed > 0 {
				var cipher []byte
				if e = f.pool.QueryRow(ctx, "SELECT link_encrypted FROM release_candidate WHERE guid='bounded-0'").Scan(&cipher); e != nil || !bytes.Equal(firstCipher, cipher) {
					t.Fatal("rejected page changed prior candidate", e)
				}
			}
			if tc.name == "rate limited" {
				var cooling bool
				if e = f.pool.QueryRow(ctx, "SELECT next_query_at>clock_timestamp()+interval '25 seconds' FROM indexer WHERE id=$1", idx.ID).Scan(&cooling); e != nil || !cooling {
					t.Fatal(cooling, e)
				}
			}
		})
	}
}

func TestNativeAggregateContinuationWaitDoesNotOwnPhysicalPermit(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var calls atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, e := fmt.Fprint(w, `<rss><channel><response offset="0" total="51"/>`+boundedPageItems(0, 50)+`</channel></rss>`)
		if e != nil {
			t.Error(e)
		}
	}))
	defer stub.Close()
	svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 7*time.Second)
	idx, e := svc.CreateIndexer(ctx, "budget wait", stub.URL, "synthetic-key", nil)
	if e != nil {
		t.Fatal(e)
	}
	svc.RegisterClient(idx.ID, torznab.NewClient(stub.URL, "synthetic-key", stub.Client()))
	done := make(chan error, 1)
	go func() { _, _, e := svc.AggregateSearchDetailed(ctx, "Fight Club"); done <- e }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("aggregate owner not joined")
			}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		e = f.pool.QueryRow(ctx, "SELECT count(*) FROM indexer_query_page").Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first page did not commit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	observer, err := pgx.Connect(ctx, f.pool.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observer.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	phaseDeadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND query LIKE $1 AND state='idle')`, "-- name: GetSearchContinuationDelay :one%").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(phaseDeadline) {
			t.Fatal("continuation budget phase not observed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var cooling bool
	if err := observer.QueryRow(ctx, `SELECT next_query_at>clock_timestamp()+interval '2 seconds' FROM indexer WHERE id=$1`, idx.ID).Scan(&cooling); err != nil || !cooling {
		t.Fatal("budget wait window unavailable", err)
	}
	// Probe after the actual continuation budget read, within its cooling window.
	permitDeadline := time.Now().Add(2 * time.Second)
	var release func()
	for {
		release, e = postgres.AcquireIndexerPermit(ctx, f.pool, idx.ID)
		if e == nil {
			break
		}
		if !errors.Is(e, ports.ErrUnavailable) || time.Now().After(permitDeadline) {
			t.Fatal("continuation wait holds physical permit", e)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	release()
	e = <-done
	joined = true
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	var count int
	if e = f.pool.QueryRow(context.Background(), "SELECT count(*) FROM indexer_query").Scan(&count); e != nil || count != 1 || calls.Load() != 1 {
		t.Fatal(count, calls.Load(), e)
	}
}
