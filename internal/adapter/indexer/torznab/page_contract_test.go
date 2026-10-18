package torznab_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTorznabPageEvidenceRejectsWrongOrAmbiguousWindow(t *testing.T) {
	const item = `<item><title>Fight.Club.1999.1080p.BluRay.x264-GROUP</title><guid>page-item</guid><link>https://example.invalid/get?passkey=synthetic-page-secret</link></item>`
	cases := []struct {
		name, body    string
		offset, limit int32
		good          bool
		count         int
	}{
		{"legacy first page", `<rss><channel>` + item + `</channel></rss>`, 0, 50, true, 1},
		{"legacy empty first page", `<rss><channel/></rss>`, 0, 50, true, 0},
		{"later page", `<rss xmlns:n="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><n:response offset="50" total="51"/>` + item + `</channel></rss>`, 50, 50, true, 1},
		{"past end", `<rss><channel><response offset="100" total="51"/></channel></rss>`, 100, 50, true, 0},
		{"unknown total", `<rss><channel><response offset="50"/>` + item + `</channel></rss>`, 50, 50, true, 1},
		{"replayed first page", `<rss><channel><response offset="0" total="51"/>` + item + `</channel></rss>`, 50, 50, false, 0},
		{"missing later proof", `<rss><channel>` + item + `</channel></rss>`, 50, 50, false, 0},
		{"missing channel", `<rss/>`, 0, 50, false, 0},
		{"duplicate channels", `<rss><channel/><channel/></rss>`, 0, 50, false, 0},
		{"duplicate response", `<rss><channel><response offset="50"/><response offset="50"/></channel></rss>`, 50, 50, false, 0},
		{"missing offset", `<rss><channel><response total="1"/>` + item + `</channel></rss>`, 0, 50, false, 0},
		{"negative total", `<rss><channel><response offset="0" total="-1"/></channel></rss>`, 0, 50, false, 0},
		{"overflow total", `<rss><channel><response offset="0" total="9223372036854775808"/></channel></rss>`, 0, 50, false, 0},
		{"count beyond total", `<rss><channel><response offset="50" total="50"/>` + item + `</channel></rss>`, 50, 50, false, 0},
		{"empty before total", `<rss><channel><response offset="0" total="1"/></channel></rss>`, 0, 50, false, 0},
		{"over limit", `<rss><channel>` + item + item + `</channel></rss>`, 0, 1, false, 0},
		{"trailing error", `<rss><channel/></rss><error code="100" description="synthetic-page-secret"/>`, 0, 50, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, tc.body) }))
			defer server.Close()
			got, e := torznab.NewClient(server.URL, "synthetic-page-key", nil).Search(context.Background(), "Fight Club", nil, tc.offset, tc.limit)
			if tc.good {
				if e != nil || len(got) != tc.count {
					t.Fatalf("valid page: count=%d error=%v", len(got), e)
				}
			} else if !errors.Is(e, ports.ErrBadResponse) || len(got) != 0 {
				t.Fatalf("unproven page accepted: count=%d error=%v", len(got), e)
			}
		})
	}
}
func TestTorznabInvalidPageWindowSendsNoRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `<rss><channel/></rss>`)
	}))
	defer server.Close()
	client := torznab.NewClient(server.URL, "synthetic-page-key", nil)
	for _, window := range [][2]int32{{-1, 50}, {0, -1}} {
		if _, e := client.Search(context.Background(), "", nil, window[0], window[1]); !errors.Is(e, ports.ErrBadResponse) {
			t.Fatalf("invalid window accepted: %v", e)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid window sent HTTP")
	}
}
