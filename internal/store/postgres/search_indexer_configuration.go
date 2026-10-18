package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// executionIndexer resolves the exact execution URL in the caller's snapshot.
// The base model stays unchanged for historical schema and digest compatibility.
func (s *SearchExecutionStore) executionIndexer(ctx context.Context, q *db.Queries, row db.Indexer) (ports.SearchIndexer, error) {
	resolved, modes, err := ResolveExecutableIndexerConfiguration(ctx, q, s.decoder, row)
	if err != nil {
		return ports.SearchIndexer{}, err
	}
	out := searchIndexerDTO(resolved)
	out.Protocol = modes.Protocol
	out.LegacyV1 = modes.LegacyV1
	return out, nil
}
