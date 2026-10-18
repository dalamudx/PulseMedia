package search_test

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"testing"
)

func TestClassifyLegacyIndexerProtocolPreservesV1Priority(t *testing.T) {
	tests := []struct {
		name, url, key string
		want           ports.IndexerProtocol
	}{
		{"empty_public_api", "http://127.0.0.1/api", "", ports.IndexerProtocolTorznab},
		{"normal", "https://fixture.invalid/api", "token", ports.IndexerProtocolTorznab},
		{"equals_cookie", "https://fixture.invalid/api", "token=encoded", ports.IndexerProtocolNexusPHP},
		{"semicolon_cookie", "https://fixture.invalid/api", "cookie;token", ports.IndexerProtocolNexusPHP},
		{"passkey_cookie", "https://fixture.invalid/api", "passkey", ports.IndexerProtocolNexusPHP},
		{"public_marker", "https://fixture.invalid/api", "public", ports.IndexerProtocolRSS},
		{"template_priority", "https://fixture.invalid/{query}", "token=encoded", ports.IndexerProtocolRSS},
		{"keyword_template", "https://fixture.invalid/{keyword}", "", ports.IndexerProtocolRSS},
		{"legacy_site_priority", "https://mikanani.me/feed", "token=encoded", ports.IndexerProtocolRSS},
		{"legacy_bangumi", "https://bangumi.moe/feed", "", ports.IndexerProtocolRSS},
		{"legacy_dmhy", "https://dmhy.org/feed", "", ports.IndexerProtocolRSS},
		{"legacy_nyaa", "https://nyaa.si/feed", "", ports.IndexerProtocolRSS},
		{"legacy_acg", "https://acg.rip/feed", "", ports.IndexerProtocolRSS},
		{"case_exact", "https://fixture.invalid/api", "PUBLIC", ports.IndexerProtocolTorznab},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := search.ClassifyLegacyIndexerProtocol(tt.url, tt.key); got != tt.want {
				t.Fatalf("protocol=%s want=%s", got, tt.want)
			}
		})
	}
}
