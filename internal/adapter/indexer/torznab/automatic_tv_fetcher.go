package torznab

import (
	"context"
	"net/http"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// AutomaticTVTorrentFetcher only fetches bytes; it grants no send authority.
type AutomaticTVTorrentFetcher struct{ httpClient *http.Client }

var _ ports.AutomaticTVTorrentFetcher = (*AutomaticTVTorrentFetcher)(nil)

func NewAutomaticTVTorrentFetcher(httpClient *http.Client) *AutomaticTVTorrentFetcher {
	client := http.Client{Timeout: 15 * time.Second}
	if httpClient != nil {
		client = *httpClient
	}
	if client.Timeout <= 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	return &AutomaticTVTorrentFetcher{httpClient: &client}
}

func (f *AutomaticTVTorrentFetcher) FetchAutomaticTVTorrent(ctx context.Context, source ports.AutomaticTVTorrentSource) ([]byte, error) {
	return NewClient(source.EndpointURL, source.APIKey, f.httpClient).FetchTorrent(ctx, source.DownloadURL)
}
