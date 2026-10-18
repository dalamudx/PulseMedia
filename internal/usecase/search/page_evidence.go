package search

import "github.com/dalamudx/PulseMedia/internal/ports"

func validPageProof(p *ports.IndexerPage, offset, limit int32) bool {
	if p == nil || p.Offset != offset || p.Limit != limit || len(p.Items) > int(limit) {
		return false
	}
	if !p.OffsetVerified {
		return p.Total == nil && offset == 0
	}
	if p.Total == nil {
		return true
	}
	total, count, start := *p.Total, int64(len(p.Items)), int64(offset)
	return total >= 0 && ((count == 0 && start >= total) || (count > 0 && start < total && count <= total-start))
}
