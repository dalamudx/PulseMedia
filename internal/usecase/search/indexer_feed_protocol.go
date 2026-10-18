package search

import "github.com/dalamudx/PulseMedia/internal/ports"

// configuredRSSKind preserves the frozen V1 RSS mode, while explicit new
// configurations select the matching feed implementation. A NexusPHP feed
// requires an explicitly configured RSS URL instead of guessing an API protocol.
func configuredRSSKind(indexer ports.SearchIndexer) (string, error) {
	if indexer.LegacyV1 || indexer.Protocol == "" {
		return indexer.RssKind, nil
	}
	switch indexer.Protocol {
	case ports.IndexerProtocolRSS:
		return "rss", nil
	case ports.IndexerProtocolTorznab:
		return indexer.RssKind, nil
	case ports.IndexerProtocolNexusPHP:
		if indexer.RssKind == "rss" && len(indexer.RssUrlEncrypted) > 0 {
			return "rss", nil
		}
	}
	return "", ports.ErrUnavailable
}
