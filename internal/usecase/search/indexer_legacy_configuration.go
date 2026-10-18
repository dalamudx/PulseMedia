package search

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
)

// ClassifyLegacyIndexerProtocol is only used by the one-time V1 data conversion.
// New configurations must supply an explicit protocol. Keep this order identical
// to the V1 constructor: an RSS template/public marker wins over cookie syntax.
func ClassifyLegacyIndexerProtocol(baseURL, credential string) ports.IndexerProtocol {
	if strings.Contains(baseURL, "{query}") || strings.Contains(baseURL, "{keyword}") ||
		strings.Contains(baseURL, "mikanani.me") || strings.Contains(baseURL, "bangumi.moe") ||
		strings.Contains(baseURL, "dmhy.org") || strings.Contains(baseURL, "nyaa.si") ||
		strings.Contains(baseURL, "acg.rip") || credential == "public" {
		return ports.IndexerProtocolRSS
	}
	if strings.Contains(credential, ";") || strings.Contains(credential, "=") || strings.Contains(credential, "passkey") {
		return ports.IndexerProtocolNexusPHP
	}
	return ports.IndexerProtocolTorznab
}
