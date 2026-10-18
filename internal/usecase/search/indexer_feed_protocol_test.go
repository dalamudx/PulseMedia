package search

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
)

func TestConfiguredRSSProtocolPreservesLegacyAndRefusesUnsupportedAPI(t *testing.T) {
	tests := []struct {
		name   string
		source ports.SearchIndexer
		want   string
		fail   bool
	}{
		{"legacy_torznab_mode", ports.SearchIndexer{LegacyV1: true, Protocol: ports.IndexerProtocolNexusPHP, RssKind: "torznab"}, "torznab", false},
		{"legacy_feed_mode", ports.SearchIndexer{LegacyV1: true, Protocol: ports.IndexerProtocolTorznab, RssKind: "rss"}, "rss", false},
		{"legacy_fixture", ports.SearchIndexer{RssKind: "torznab"}, "torznab", false},
		{"explicit_feed", ports.SearchIndexer{Protocol: ports.IndexerProtocolRSS, RssKind: "torznab"}, "rss", false},
		{"explicit_torznab", ports.SearchIndexer{Protocol: ports.IndexerProtocolTorznab, RssKind: "torznab"}, "torznab", false},
		{"torznab_feed_override", ports.SearchIndexer{Protocol: ports.IndexerProtocolTorznab, RssKind: "rss", RssUrlEncrypted: []byte{1}}, "rss", false},
		{"nexus_api_refused", ports.SearchIndexer{Protocol: ports.IndexerProtocolNexusPHP, RssKind: "torznab"}, "", true},
		{"nexus_missing_override_refused", ports.SearchIndexer{Protocol: ports.IndexerProtocolNexusPHP, RssKind: "rss"}, "", true},
		{"nexus_feed_override", ports.SearchIndexer{Protocol: ports.IndexerProtocolNexusPHP, RssKind: "rss", RssUrlEncrypted: []byte{1}}, "rss", false},
		{"unknown_refused", ports.SearchIndexer{Protocol: "invalid", RssKind: "torznab"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, err := configuredRSSKind(tt.source)
			if kind != tt.want || (tt.fail && !errors.Is(err, ports.ErrUnavailable)) || (!tt.fail && err != nil) {
				t.Fatalf("kind=%q unavailable=%t", kind, errors.Is(err, ports.ErrUnavailable))
			}
		})
	}
}
