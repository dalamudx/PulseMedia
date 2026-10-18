package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
)

func resolveUpgradeSourceURL(ctx context.Context, tx pgx.Tx, b *preparationBasis, storedURL string) (string, error) {
	full, modes, err := ResolveIndexerURL(ctx, db.New(tx), b.indexerDecoder, b.snapshot.SourceID, b.snapshot.Target.Candidate.SourceVersion, storedURL)
	if err != nil {
		return "", ports.ErrUnavailable
	}
	// V1 dedicated preparation always used Torznab. New configurations explicitly
	// select their protocol; unsupported dedicated acquisition is refused.
	if !modes.LegacyV1 && modes.Protocol != ports.IndexerProtocolTorznab {
		return "", preparationConflict()
	}
	return full, nil
}
