package ports

import (
	"context"
)

// TorrentInfo represents the current status of a torrent inside the download client.
type TorrentInfo struct {
	InfoHash        string  `json:"info_hash"`
	Name            string  `json:"name"`
	SavePath        string  `json:"save_path"`
	ContentPath     string  `json:"content_path,omitempty"`
	Progress        float32 `json:"progress"`
	TotalBytes      int64   `json:"total_bytes"`
	DownloadedBytes int64   `json:"downloaded_bytes"`
	UploadedBytes   int64   `json:"uploaded_bytes"`
	Ratio           float64 `json:"ratio"`
	State           string  `json:"state"`
	Tags            string  `json:"tags,omitempty"`
}

// AddTorrentParam contains parameters for adding a new torrent task.
type AddTorrentParam struct {
	InfoHash     string `json:"info_hash"`
	TorrentBytes []byte `json:"-"`
	DownloadURL  string `json:"download_url,omitempty"`
	SavePath     string `json:"save_path"`
	Tags         string `json:"tags,omitempty"`
	Paused       bool   `json:"paused"`
}

// DownloadClient abstracts a BitTorrent download client (e.g. qBittorrent).
type DownloadClient interface {
	AddTorrent(ctx context.Context, p AddTorrentParam) error
	GetTorrent(ctx context.Context, infoHash string) (*TorrentInfo, error)
	ListTorrents(ctx context.Context, tag string) ([]*TorrentInfo, error)
	RemoveTorrent(ctx context.Context, infoHash string, deleteFiles bool) error
	Test(ctx context.Context) error
}
