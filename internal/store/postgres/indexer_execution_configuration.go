package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

// ResolveExecutableIndexerConfiguration excludes disabled/tombstoned sources in
// the caller's snapshot. Historical reconstruction uses the ungated resolver.
func ResolveExecutableIndexerConfiguration(ctx context.Context, q *db.Queries, decoder ports.CredentialDecoder, row db.Indexer) (db.Indexer, ports.IndexerExecutionModes, error) {
	available, err := IndexerAvailableForExecution(ctx, q, row.ID)
	if err != nil {
		return db.Indexer{}, ports.IndexerExecutionModes{}, err
	}
	if !available {
		return db.Indexer{}, ports.IndexerExecutionModes{}, ports.ErrNotFound
	}
	if err := cookieCloudCredentialUsable(ctx, q, row); err != nil {
		return db.Indexer{}, ports.IndexerExecutionModes{}, err
	}
	return ResolveIndexerConfiguration(ctx, q, decoder, row)
}
