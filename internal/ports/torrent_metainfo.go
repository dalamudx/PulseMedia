package ports

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

const MaxTorrentMetainfoBytes = 10 << 20

var ErrInvalidTorrentMetainfo = errors.New("torrent metainfo requires manual review")

// TorrentMetainfo describes the exact inspected v1 bytes, never a URL to fetch again.
// It is file evidence only; submission still requires current transactional authority.
type TorrentMetainfo struct {
	TorrentBytes []byte                `json:"-"`
	BytesSHA256  [32]byte              `json:"-"`
	InfoHash     string                `json:"-"`
	TotalBytes   int64                 `json:"-"`
	Files        []media.TVTorrentFile `json:"-"`
}

type TorrentMetainfoInspector interface {
	InspectTorrent([]byte) (*TorrentMetainfo, error)
}
