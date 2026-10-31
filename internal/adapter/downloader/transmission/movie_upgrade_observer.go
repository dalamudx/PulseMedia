package transmission

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// MovieUpgradeObserver cannot send or establish completion authority.
func (c *Client) MovieUpgradeObserver() ports.MovieUpgradeTorrentObserver {
	return movieUpgradeObservationClient{dedicatedManifestReader{automaticTVReader{client: c}}}
}

type movieUpgradeObservationClient struct{ native dedicatedManifestReader }

func (c movieUpgradeObservationClient) ListMovieUpgradeTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	return c.native.ListTorrents(ctx, tag)
}
