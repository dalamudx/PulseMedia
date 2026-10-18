package app_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func TestNativeTorznabAggregateDoesNotCertifyFirstPage(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx := context.Background()
	var body atomic.Value
	item := `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>complete-proof</guid><link>https://fixture.invalid/?passkey=synthetic-completeness</link></item>`
	body.Store(`<rss><channel><response offset="0" total="51"/>` + item + `</channel></rss>`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body.Load().(string)) }))
	defer server.Close()
	svc := search.NewService(postgres.NewSearchExecutionStore(f.pool), idgen.New(), crypto.NewSecretProtector(bytes.Repeat([]byte{7}, 32)), 0)
	idx, e := svc.CreateIndexer(ctx, "completeness fixture", server.URL, "synthetic-key", nil)
	if e != nil {
		t.Fatal(e)
	}
	svc.RegisterClient(idx.ID, torznab.NewClient(server.URL, "synthetic-key", server.Client()))
	for _, tc := range []struct {
		name, body string
		partial    bool
		count      int
	}{
		{"more", `<response offset="0" total="51"/>` + item, true, 1},
		{"unknown", item, true, 1},
		{"empty unknown", "", true, 0},
		{"complete", `<response offset="0" total="1"/>` + item, false, 1},
		{"empty complete", `<response offset="0" total="0"/>`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body.Store(`<rss><channel>` + tc.body + `</channel></rss>`)
			rows, partial, e := svc.AggregateSearchDetailed(ctx, "Fight Club")
			if e != nil || partial != tc.partial || len(rows) != tc.count {
				t.Fatalf("rows=%d partial=%v expected=%v err=%v", len(rows), partial, tc.partial, e)
			}
		})
	}
}
