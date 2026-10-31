package transmission

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// CompleteTVObserver supplies a bounded complete lookup for unknown-send R3.
func (c *Client) CompleteTVObserver() ports.DownloadClient {
	return &completeTVObservationClient{Client: c, dedicatedManifestReader: dedicatedManifestReader{automaticTVReader{client: c}}}
}

type completeTVObservationClient struct {
	*Client
	dedicatedManifestReader
}

func (c *completeTVObservationClient) ListTorrents(ctx context.Context, tag string) ([]*ports.TorrentInfo, error) {
	return c.dedicatedManifestReader.ListTorrents(ctx, tag)
}
