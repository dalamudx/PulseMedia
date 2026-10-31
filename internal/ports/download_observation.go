package ports

import (
	"context"
	"github.com/google/uuid"
)

// Observation clients expose reads only. Preparation retains the physical budget
// through all native reads and the final database callback.
type DownloadObservationClient interface {
	GetTorrent(context.Context, string) (*TorrentInfo, error)
	ListTorrents(context.Context, string) ([]*TorrentInfo, error)
}
type DownloadObservationResult struct {
	Info   *TorrentInfo
	Err    error
	Files  []TorrentFile
	Second *TorrentInfo
}
type PreparedDownloadObservation struct {
	Task              *DownloadExecutionTask
	Client            DownloadObservationClient
	InventoryRequired func(context.Context, *TorrentInfo) (bool, error)
	Complete          func(context.Context, DownloadObservationResult) (*DownloadExecutionTask, error)
	Release           func()
}
type DownloadObservationPreparer interface {
	PrepareDownloadObservation(context.Context, uuid.UUID, bool, *DownloadWatchTarget) (*PreparedDownloadObservation, error)
}
