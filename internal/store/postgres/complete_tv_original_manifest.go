package postgres

import (
	"math"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// This list is loaded solely from the immutable preparation created from the
// private inspected proof before the original Add. It is never supplied by a
// native adapter, public command or recovered task payload. Legacy preparations
// without the complete original manifest fail closed.
func trackingOriginalFiles(p completeTVPreparedProof, files []ports.TorrentFile) bool {
	if len(p.OriginalFiles) < 2 || len(p.OriginalFiles) > 4096 || p.TorrentSHA256 == [32]byte{} {
		return false
	}
	var total int64
	for _, f := range p.OriginalFiles {
		if f.Size < 0 || f.Size > math.MaxInt64-total {
			return false
		}
		total += f.Size
	}
	return total == p.TotalBytes && trackingManifestFiles(p, p.OriginalFiles, files)
}
