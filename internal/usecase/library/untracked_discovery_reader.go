package library

import (
	"context"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type UntrackedDiscoveryReader struct {
	repo ports.UntrackedDiscoveryReader
}

func NewUntrackedDiscoveryReader(repo ports.UntrackedDiscoveryReader) *UntrackedDiscoveryReader {
	return &UntrackedDiscoveryReader{repo: repo}
}

// Read returns persisted positive observations, never a filesystem inventory.
func (s *UntrackedDiscoveryReader) Read(ctx context.Context, actor *ports.Actor, scopeID uuid.UUID, cursor string, limit int) (ports.UntrackedDiscoveryPage, error) {
	var zero ports.UntrackedDiscoveryPage
	current, err := untrackedScopeActor(ctx, actor, scopeID)
	if err != nil {
		return zero, err
	}
	if len(cursor) > 2048 || strings.ContainsAny(cursor, "\x00\r\n\t") || limit < 0 || limit > 64 {
		return zero, ports.ErrRejected
	}
	if limit == 0 {
		limit = 64
	}
	if s == nil || s.repo == nil {
		return zero, ports.ErrUnavailable
	}
	page, err := s.repo.ReadUntrackedDiscovery(ctx, &current, scopeID, cursor, limit)
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err != nil {
		return zero, untrackedScopeError(err)
	}
	if len(page.Files) > limit || page.Complete != (page.NextCursor == "") || len(page.NextCursor) > 2048 {
		return zero, ports.ErrUnavailable
	}
	seen := make(map[uuid.UUID]bool, len(page.Files))
	for _, file := range page.Files {
		if file.ID == uuid.Nil || seen[file.ID] || file.ScopeID != scopeID || file.ScopeVersion < 1 || file.Revision < 1 || !untrackedDiscoveryRelative(file.RelativePath) || !naming.IsVideoPath(file.RelativePath) || !untrackedScopeIdentity(file.Identity) || file.Identity.Size < 0 || file.Observations < 1 || file.FirstObservedAt.IsZero() || file.LastObservedAt.Before(file.FirstObservedAt) {
			return zero, ports.ErrUnavailable
		}
		seen[file.ID] = true
	}
	page.Files = append([]ports.UntrackedDiscoveredFile{}, page.Files...)
	return page, nil
}
