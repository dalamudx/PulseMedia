package universal_rss_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/universal_rss"
)

func TestUniversalRSSMikanParsing(t *testing.T) {
	// Sample Mikan XML with custom torrent namespace and enclosure
	mikanXML := `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0">
<channel>
<title>Mikan Project - Frieren</title>
<item>
<title>[LoliHouse] 葬送的芙莉莲 - 28 [1080p]</title>
<link>https://mikanani.me/Home/Episode/597c85</link>
<enclosure url="https://mikanani.me/Download/frieren.torrent" length="693213568" type="application/x-bittorrent" />
<torrent><contentLength>693213568</contentLength></torrent>
<guid>https://mikanani.me/Home/Episode/597c85</guid>
</item>
</channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = fmt.Fprint(w, mikanXML)
	}))
	defer server.Close()

	// 1. Test custom template URL
	client := universal_rss.NewClient(server.URL+"/?searchstr={query}", server.Client())
	ctx := context.Background()

	items, err := client.Search(ctx, "Frieren", nil, 0, 20)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Title != "[LoliHouse] 葬送的芙莉莲 - 28 [1080p]" {
		t.Fatalf("unexpected title: %s", items[0].Title)
	}
	if items[0].SizeBytes != 693213568 {
		t.Fatalf("unexpected size: %d", items[0].SizeBytes)
	}
}

func TestUniversalRSSBangumiMoeAndMagnetParsing(t *testing.T) {
	bangumiXML := `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0">
<channel>
<title>Bangumi.moe Latest</title>
<item>
<title>[喵萌奶茶屋] 鬼灭之刃 柱训练篇 - 01 [1080p]</title>
<link>https://bangumi.moe/torrent/12345</link>
<enclosure url="magnet:?xt=urn:btih:88AABBCCDDEEFF0011223344556677889900AABB&amp;dn=DemonSlayer" length="500000000" type="application/x-bittorrent" />
<guid>https://bangumi.moe/torrent/12345</guid>
</item>
</channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = fmt.Fprint(w, bangumiXML)
	}))
	defer server.Close()

	client := universal_rss.NewClient(server.URL+"/{query}", server.Client())
	ctx := context.Background()

	items, err := client.Search(ctx, "DemonSlayer", nil, 0, 20)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].InfoHash != "88aabbccddeeff0011223344556677889900aabb" {
		t.Fatalf("unexpected infohash: %s", items[0].InfoHash)
	}
}

func TestDeclarativeRuleResolution(t *testing.T) {
	client := universal_rss.NewClient("https://mikanani.me", nil)
	url := client.BuildSearchURL("芙莉莲")
	if !strings.Contains(url, "mikanani.me/RSS/Search?searchstr=") {
		t.Fatalf("unexpected resolved url: %s", url)
	}

	bClient := universal_rss.NewClient("https://bangumi.moe", nil)
	bURL := bClient.BuildSearchURL("鬼灭之刃")
	if !strings.Contains(bURL, "bangumi.moe/rss/tags/") {
		t.Fatalf("unexpected resolved url: %s", bURL)
	}
}
