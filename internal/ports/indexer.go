package ports

import (
	"context"
	"time"
)

// ReleaseItem represents a single torrent release returned by an indexer.
type ReleaseItem struct {
	Title       string     `json:"title"`
	GUID        string     `json:"guid"`
	DownloadURL string     `json:"download_url"`
	InfoHash    string     `json:"info_hash,omitempty"`
	SizeBytes   int64      `json:"size_bytes"`
	Seeders     int32      `json:"seeders"`
	Leechers    int32      `json:"leechers"`
	PublishDate *time.Time `json:"publish_date,omitempty"`
	Categories  []int32    `json:"categories,omitempty"`
}

// IndexerClient abstracts communication with a Torznab/Newznab indexer.
type IndexerClient interface {
	Search(ctx context.Context, query string, categories []int32, offset, limit int32) ([]*ReleaseItem, error)
	FetchTorrent(ctx context.Context, downloadURL string) ([]byte, error)
}
