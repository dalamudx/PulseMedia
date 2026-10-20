package watchlist

import (
	"slices"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func validBindings(b *ports.DoubanSyncBindings) bool {
	if b == nil {
		return true
	}
	if b.IndexerIDs == nil || len(b.IndexerIDs) > 64 {
		return false
	}
	if len(b.IndexerIDs) == 0 {
		return b.DownloaderID == nil
	}
	if b.DownloaderID == nil || *b.DownloaderID == uuid.Nil {
		return false
	}
	seen := make(map[uuid.UUID]bool, len(b.IndexerIDs))
	for _, id := range b.IndexerIDs {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func cloneBindings(b *ports.DoubanSyncBindings) *ports.DoubanSyncBindings {
	if b == nil {
		return nil
	}
	return &ports.DoubanSyncBindings{IndexerIDs: slices.Clone(b.IndexerIDs), DownloaderID: cloneRoot(b.DownloaderID)}
}

func equalBindings(a, b *ports.DoubanSyncBindings) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return (a.IndexerIDs == nil) == (b.IndexerIDs == nil) && slices.Equal(a.IndexerIDs, b.IndexerIDs) && equalRoot(a.DownloaderID, b.DownloaderID)
}
