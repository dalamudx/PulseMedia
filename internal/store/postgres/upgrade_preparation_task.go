package postgres

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The complete expected task row is constructed before INSERT. Known zero
// counters and the pending organization state are explicit; optional fields
// remain null and unknown mandatory additions fail closed.
func preparationTaskTemplate(
	ctx context.Context, tx pgx.Tx, b *preparationBasis, id uuid.UUID,
	infoHash string, totalBytes int64,
) (json.RawMessage, error) {
	base, err := json.Marshal(map[string]any{
		"id": id, "operation_id": b.operation,
		"candidate_id": b.snapshot.CandidateID,
		"client_id":    b.snapshot.ClientID,
		"state":        "submitting",
		"save_path":    b.savePath,
		"info_hash":    infoHash,
		"total_bytes":  totalBytes,
		"progress":     0, "downloaded_bytes": 0, "reconcile_count": 0,
		"organization_state": "pending",
	})
	if err != nil {
		return nil, err
	}
	var full json.RawMessage
	var acceptsAllFields bool
	err = tx.QueryRow(ctx, `
WITH stamp AS MATERIALIZED (SELECT clock_timestamp() AS now),
expected AS (
 SELECT to_jsonb(jsonb_populate_record(NULL::download_task,
  $1::jsonb || jsonb_build_object('created_at',stamp.now,'updated_at',stamp.now))) AS row
 FROM stamp
)
SELECT row,row @> $1::jsonb FROM expected`, base).Scan(&full, &acceptsAllFields)
	if err != nil {
		return nil, err
	}
	if !acceptsAllFields {
		return nil, preparationConflict()
	}
	return full, nil
}

func insertPreparationTask(
	ctx context.Context, tx pgx.Tx, id uuid.UUID, expected json.RawMessage,
) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM download_task WHERE id=$1)",
		id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return preparationConflict()
	}
	var actual json.RawMessage
	err := tx.QueryRow(ctx, `
WITH written AS (
 INSERT INTO download_task
 SELECT (jsonb_populate_record(NULL::download_task,$1::jsonb)).*
 RETURNING *
)
SELECT to_jsonb(written) FROM written`, expected).Scan(&actual)
	if err != nil {
		return err
	}
	// The expectation existed before INSERT. A corrupting INSERT trigger
	// cannot redefine the first accepted task snapshot.
	if !preparationSameJSON(actual, expected) {
		return preparationConflict()
	}
	return nil
}
