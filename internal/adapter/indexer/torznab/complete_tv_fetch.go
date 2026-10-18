package torznab

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// CompleteTVFetcher retains the native bounded, redirect-refusing fetch contract.
type CompleteTVFetcher struct{}

func (CompleteTVFetcher) FetchCompleteTVTorrent(ctx context.Context, source ports.CompleteTVTorrentSource) ([]byte, error) {
	return NewClient(source.EndpointURL, source.APIKey, nil).FetchTorrent(ctx, source.DownloadURL)
}

var _ ports.CompleteTVTorrentFetcher = CompleteTVFetcher{}
