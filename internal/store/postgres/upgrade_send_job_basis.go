package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
)

// A verified command is the only exception to the original zero-job preparation graph.
// Both complete rows are frozen independently of the persistent SQL guard.
func bindSendJobBasis(ctx context.Context, tx pgx.Tx, b *preparationBasis) error {
	var installed bool
	if e := tx.QueryRow(ctx, `SELECT to_regclass('movie_upgrade_send_job') IS NOT NULL`).Scan(&installed); e != nil {
		return e
	}
	if !installed {
		return nil
	}
	var command, job json.RawMessage
	var valid bool
	e := tx.QueryRow(ctx, `SELECT to_jsonb(c),to_jsonb(j),pulse_is_upgrade_send_job(j) FROM movie_upgrade_send_job c JOIN job j ON j.id=c.job_id WHERE c.operation_id=$1`, b.operation).Scan(&command, &job, &valid)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if !valid {
		return preparationConflict()
	}
	b.sendCommand, b.sendJob = command, job
	return nil
}
func (b *preparationBasis) checkSendJobBasis(ctx context.Context, tx pgx.Tx) (int, error) {
	if len(b.sendJob) == 0 && len(b.sendCommand) == 0 {
		return 0, nil
	}
	var valid bool
	e := tx.QueryRow(ctx, `SELECT to_jsonb(c)=$2::jsonb AND to_jsonb(j)=$3::jsonb AND pulse_is_upgrade_send_job(j) FROM movie_upgrade_send_job c JOIN job j ON j.id=c.job_id WHERE c.operation_id=$1`, b.operation, b.sendCommand, b.sendJob).Scan(&valid)
	if e != nil {
		return 0, e
	}
	if !valid {
		return 0, preparationConflict()
	}
	return 1, nil
}
