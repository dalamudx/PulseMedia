package qbittorrent

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Only the bounded native listing/strict JSON decoder is reused. This is not a
// TV coverage or submission authorization, and it exposes no downloader writes.
func (c *Client) MovieUpgradeObserver() ports.MovieUpgradeTorrentObserver {
	return &movieUpgradeObservationClient{native: &completeTVObservationClient{c}}
}

type movieUpgradeObservationClient struct{ native *completeTVObservationClient }

func (c *movieUpgradeObservationClient) ListMovieUpgradeTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	return c.native.ListTorrents(ctx, tag)
}
