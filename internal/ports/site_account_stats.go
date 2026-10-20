package ports

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
)

// SiteAccountStats preserves source display precision, not exact transfer bytes.
type SiteAccountStats = indexer.SiteAccountStats

const NexusPHPClassicAccountStatsProfile = "nexusphp_classic_info_block_v1"

// SiteAccountStatsClient verifies the explicitly configured remote account.
// Credentials and source URLs come from the configured client, never this call.
type SiteAccountStatsClient interface {
	ReadAccountStats(ctx context.Context, expectedAccountID string) (SiteAccountStats, error)
}
