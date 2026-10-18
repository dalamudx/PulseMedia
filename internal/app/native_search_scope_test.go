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

func TestNativeSearchRejectsConfigChangeDuringResponse(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 2 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = fmt.Fprintf(w, `<rss><channel><response offset="0" total="1"/><item><title>Fight.Club.1999.1080p.BluRay</title><guid>stable-source</guid><link>https://fixture.invalid/?passkey=synthetic</link><attr name="seeders" value="%d"/></item></channel></rss>`, n)
	}))
	defer stub.Close()
	svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 0)
	idx, e := svc.CreateIndexer(ctx, "scope", stub.URL, "synthetic", nil)
	if e != nil {
		t.Fatal(e)
	}
	svc.RegisterClient(idx.ID, torznab.NewClient(stub.URL, "synthetic", stub.Client()))
	if _, e = svc.ExecuteSearch(ctx, idx.ID, "Fight Club", 0); e != nil {
		t.Fatal(e)
	}
	snapshot := func() string {
		var s string
		if err := f.pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(c) ORDER BY id)::text FROM release_candidate c WHERE source_config_version=1`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	done := make(chan error, 1)
	go func() { _, _, err := svc.AggregateSearchDetailed(ctx, "Fight Club"); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, e = f.pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", idx.ID); e != nil {
		t.Fatal(e)
	}
	close(release)
	select {
	case e = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("old config returned as usable result: %v", e)
	}
	if snapshot() != before {
		t.Fatal("old response changed historical candidate")
	}
	var proofs, failed int
	if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM indexer_query_page),(SELECT count(*) FROM indexer_query WHERE state='failed')`).Scan(&proofs, &failed); e != nil || proofs != 1 || failed != 1 {
		t.Fatal(proofs, failed, e)
	}
	rows, partial, e := svc.AggregateSearchDetailed(ctx, "Fight Club")
	if e != nil || partial || len(rows) != 1 || rows[0].SourceConfigVersion != 2 {
		t.Fatal(len(rows), partial, e)
	}
	if snapshot() != before {
		t.Fatal("new config retry rewrote history")
	}
}
