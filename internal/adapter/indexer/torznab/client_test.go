package torznab_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestTorznabXMLSearchAndAttributes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		// Verify API key was passed in request
		apiKey := r.URL.Query().Get("apikey")
		if apiKey != "secret-passkey" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Return standard Torznab XML feed
		xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>Mock Indexer</title>
    <item>
      <title>Fight.Club.1999.1080p.BluRay.x264-SPARKS</title>
      <guid>mock-guid-550</guid>
      <link>https://mock-indexer.com/download/550.torrent</link>
      <enclosure url="https://mock-indexer.com/download/550.torrent" length="8589934592" type="application/x-bittorrent" />
      <pubDate>Mon, 05 Oct 2026 12:00:00 +0000</pubDate>
      <torznab:attr name="infohash" value="9A3F12C45B6D7E8F0123456789ABCDEF01234567" />
      <torznab:attr name="seeders" value="42" />
      <torznab:attr name="peers" value="5" />
      <torznab:attr name="category" value="2000" />
      <torznab:attr name="category" value="2040" />
    </item>
  </channel>
</rss>`
		_, _ = fmt.Fprint(w, xmlData)
	}))
	defer server.Close()

	client := torznab.NewClient(server.URL, "secret-passkey", nil)
	ctx := context.Background()

	results, err := client.Search(ctx, "Fight Club", []int32{2000}, 0, 50)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	rel := results[0]
	if rel.Title != "Fight.Club.1999.1080p.BluRay.x264-SPARKS" {
		t.Fatalf("unexpected title: %s", rel.Title)
	}
	if rel.GUID != "mock-guid-550" {
		t.Fatalf("unexpected guid: %s", rel.GUID)
	}
	if rel.DownloadURL != "https://mock-indexer.com/download/550.torrent" {
		t.Fatalf("unexpected download url: %s", rel.DownloadURL)
	}
	if rel.InfoHash != "9a3f12c45b6d7e8f0123456789abcdef01234567" {
		t.Fatalf("infohash should be normalized to lowercase: got %s", rel.InfoHash)
	}
	if rel.Seeders != 42 || rel.Leechers != 5 {
		t.Fatalf("unexpected seeders/leechers: %d/%d", rel.Seeders, rel.Leechers)
	}
	if rel.SizeBytes != 8589934592 {
		t.Fatalf("unexpected size: %d", rel.SizeBytes)
	}
}

func TestTorznabRateLimitAndAuthErrors(t *testing.T) {
	// Rate limited server (429)
	rlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer rlServer.Close()

	client := torznab.NewClient(rlServer.URL, "key", nil)
	_, err := client.Search(context.Background(), "query", nil, 0, 10)
	rle, ok := ports.IsRateLimited(err)
	if !ok || rle.RetryAfter != 15*time.Second {
		t.Fatalf("expected RateLimitError with 15s, got: %v", err)
	}

	// Unauthorized server (401)
	unauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthServer.Close()

	unauthClient := torznab.NewClient(unauthServer.URL, "bad_key", nil)
	_, err = unauthClient.Search(context.Background(), "query", nil, 0, 10)
	if !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatalf("expected ErrAuthRequired on 401, got: %v", err)
	}
}
