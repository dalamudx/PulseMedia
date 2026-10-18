package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Logical terminal state is independent of the original physical endpoint lock.
// This changes only queries durably associated with this owned observation.
func failInteractiveSearchQueries(ctx context.Context, tx pgx.Tx, search, indexer uuid.UUID, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE indexer_query q SET state='failed',error=$3,finished_at=clock_timestamp() FROM interactive_search_query a WHERE q.id=a.query_id AND a.search_id=$1 AND ($2::uuid='00000000-0000-0000-0000-000000000000' OR a.indexer_id=$2) AND q.query_type='interactive' AND q.state='running'`, search, indexer, "interactive search "+reason)
	return err
}
