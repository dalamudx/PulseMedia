package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

// Preparation lease expiry is independent from a future send lease.
func bindPreparedUpgradeBasis(ctx context.Context, tx pgx.Tx, b *preparationBasis) error {
	var prepared, task json.RawMessage
	var valid bool
	err := tx.QueryRow(ctx, `SELECT to_jsonb(p),to_jsonb(t),
 p.actor_id=$2 AND p.status='prepared_not_sent' AND p.task_snapshot=to_jsonb(t)
 AND p.evidence->'authority'=$3::jsonb
 AND p.evidence->'lane'=$4::jsonb AND p.evidence->'reservation'=$5::jsonb
 AND p.evidence->'requirement'=$6::jsonb AND t.operation_id=p.operation_id
 AND l.state='prepared' AND l.actor_id=p.actor_id AND l.owner=p.owner AND l.token=p.token
 FROM movie_upgrade_preparation p JOIN download_task t ON t.id=p.task_id
 JOIN movie_upgrade_preparation_lease l ON l.operation_id=p.operation_id
 WHERE p.operation_id=$1 FOR SHARE OF p,t,l`, b.operation, b.actor.ID, b.authority, b.lane, b.reserved, b.required).Scan(&prepared, &task, &valid)
	if err != nil {
		return preparationRequiredRow(err)
	}
	if !valid {
		return preparationConflict()
	}
	b.rows["movie_upgrade_preparation"] = prepared
	b.rows["download_task"] = task
	b.taskCount, b.prepCount = 1, 1
	return nil
}
