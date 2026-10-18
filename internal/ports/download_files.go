package ports

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
)

// Full inventory, including unwanted files. A selected subset cannot prove single-file coverage.
type TorrentFile = naming.InventoryFile
type DownloadFileInventoryClient interface {
	GetTorrentFiles(context.Context, string) ([]TorrentFile, error)
}
