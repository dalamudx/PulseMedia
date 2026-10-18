package download

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// torrentSourceProtocol preserves historic RSS selection while explicit
// configurations select their source solely through the persisted protocol.
func torrentSourceProtocol(endpoint db.Indexer, modes ports.IndexerExecutionModes) string {
	if modes.Protocol == "" || (modes.LegacyV1 && endpoint.RssKind == "rss") {
		return endpoint.RssKind
	}
	return string(modes.Protocol)
}

// dedicatedTorrentSourceSupported preserves V1 dedicated Torznab acquisition.
func dedicatedTorrentSourceSupported(modes ports.IndexerExecutionModes) bool {
	return modes.Protocol == "" || modes.LegacyV1 || modes.Protocol == ports.IndexerProtocolTorznab
}
