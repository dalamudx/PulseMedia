package dmhy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/dmhy"
)

func TestDMHYPublicRSSSearch(t *testing.T) {
	mockXML := `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0">
<channel>
<title>動漫花園資源網</title>
<link>http://share.dmhy.org</link>
<item>
<title><![CDATA[[LoliHouse] 葬送的芙莉莲 / Sousou no Frieren - 28 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]]]></title>
<link>http://share.dmhy.org/topics/view/663812.html</link>
<pubDate>Sat, 23 Mar 2026 16:30:00 +0800</pubDate>
<enclosure url="magnet:?xt=urn:btih:7BSRIRFCYCZ767R5UPF5OXNFR327BHQZ&amp;dn=Frieren28" length="723485921" type="application/x-bittorrent"></enclosure>
<guid>http://share.dmhy.org/topics/view/663812.html</guid>
</item>
</channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "rss.xml") {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = fmt.Fprint(w, mockXML)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := dmhy.NewClient(server.URL, server.Client())
	ctx := context.Background()

	items, err := client.Search(ctx, "Frieren", nil, 0, 20)
	if err != nil {
		t.Fatalf("dmhy search failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	it := items[0]
	if !strings.Contains(it.Title, "葬送的芙莉莲") {
		t.Fatalf("unexpected title: %s", it.Title)
	}
	if it.InfoHash != "7bsrirfcycz767r5upf5oxnfr327bhqz" {
		t.Fatalf("unexpected infohash: %s", it.InfoHash)
	}
	if it.SizeBytes != 723485921 {
		t.Fatalf("unexpected size: %d", it.SizeBytes)
	}
}
