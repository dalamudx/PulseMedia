package app

import (
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/nexusphp"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/universal_rss"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// newConfiguredIndexerClient selects the adapter exclusively from stored protocol.
// Credentials may contain syntax belonging to another protocol without changing it.
func newConfiguredIndexerClient(protocol ports.IndexerProtocol, baseURL, credential string) ports.IndexerClient {
	switch protocol {
	case ports.IndexerProtocolTorznab:
		return torznab.NewClient(baseURL, credential, nil)
	case ports.IndexerProtocolNexusPHP:
		return nexusphp.NewClient(baseURL, credential, "", nil)
	case ports.IndexerProtocolRSS:
		return universal_rss.NewClient(baseURL, nil)
	default:
		return nil
	}
}
