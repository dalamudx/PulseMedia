package download

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

func TestTorrentSourceProtocol(t *testing.T) {
	tests := []struct {
		name    string
		rssKind string
		modes   ports.IndexerExecutionModes
		want    string
	}{
		{"historic exact empty", "", ports.IndexerExecutionModes{}, ""},
		{"historic exact custom", "custom-kind", ports.IndexerExecutionModes{}, "custom-kind"},
		{"historic RSS", "rss", ports.IndexerExecutionModes{LegacyV1: true}, "rss"},
		{"legacy RSS override", "rss", ports.IndexerExecutionModes{Protocol: ports.IndexerProtocolNexusPHP, LegacyV1: true}, "rss"},
		{"legacy classified NexusPHP", "torznab", ports.IndexerExecutionModes{Protocol: ports.IndexerProtocolNexusPHP, LegacyV1: true}, "nexusphp"},
		{"legacy RSS case exact", "RSS", ports.IndexerExecutionModes{Protocol: ports.IndexerProtocolTorznab, LegacyV1: true}, "torznab"},
		{"explicit Torznab ignores RSS", "rss", ports.IndexerExecutionModes{Protocol: ports.IndexerProtocolTorznab}, "torznab"},
		{"explicit NexusPHP", "torznab", ports.IndexerExecutionModes{Protocol: ports.IndexerProtocolNexusPHP}, "nexusphp"},
		{"explicit RSS", "torznab", ports.IndexerExecutionModes{Protocol: ports.IndexerProtocolRSS}, "rss"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Opposite credential syntax, URL hints and categories cannot select a protocol.
			for _, key := range []string{"contains=;passkey", "plain-torznab-key"} {
				for _, raw := range []string{"https://fixture.test/nexusphp?passkey=private", "https://fixture.test/torznab/api?apikey=private"} {
					endpoint := db.Indexer{RssKind: tt.rssKind, Url: raw, ApiKeyEncrypted: []byte(key), Categories: []byte("[2000,5000]")}
					if got := torrentSourceProtocol(endpoint, tt.modes); got != tt.want {
						t.Fatalf("protocol = %q, want %q", got, tt.want)
					}
				}
			}
		})
	}
}

func TestDedicatedTorrentSourceSupported(t *testing.T) {
	for _, protocol := range []ports.IndexerProtocol{"", ports.IndexerProtocolTorznab, ports.IndexerProtocolNexusPHP, ports.IndexerProtocolRSS} {
		for _, legacy := range []bool{false, true} {
			modes := ports.IndexerExecutionModes{Protocol: protocol, LegacyV1: legacy}
			want := protocol == "" || legacy || protocol == ports.IndexerProtocolTorznab
			if got := dedicatedTorrentSourceSupported(modes); got != want {
				t.Fatalf("protocol %q legacy %t supported = %t, want %t", protocol, legacy, got, want)
			}
		}
	}
}
