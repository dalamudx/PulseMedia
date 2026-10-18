package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// upgradeValidateOwned rejects INSERT triggers that corrupt an owned row before
// remember takes its first snapshot. Later corruption is caught by exact hashes.
func upgradeValidateOwned(ctx context.Context, tx pgx.Tx, table string, id uuid.UUID) error {
	var valid bool
	var e error
	switch table {
	case "acquisition_reservation":
		e = tx.QueryRow(ctx, `SELECT x.unit_id=a.unit_id AND x.source_root_id=a.root_id AND x.source_generation=a.root_generation AND x.candidate_id=a.candidate_id AND x.source_stamp=a.snapshot->>'source_stamp' AND x.acquisition_lane='default' AND x.task_id IS NULL AND NOT x.legacy_conflict AND x.released_at IS NULL FROM acquisition_reservation x JOIN movie_upgrade_authority a USING(operation_id) WHERE x.id=$1`, id).Scan(&valid)
	case "unit_reservation":
		e = tx.QueryRow(ctx, `SELECT x.unit_id=a.unit_id AND x.root_id=a.root_id AND x.candidate_id=a.candidate_id AND x.purpose='upgrade' AND x.state='reserved' AND x.expires_at IS NULL FROM unit_reservation x JOIN movie_upgrade_authority a USING(operation_id) WHERE x.id=$1`, id).Scan(&valid)
	case "download_requirement":
		e = tx.QueryRow(ctx, `SELECT x.unit_id=a.unit_id AND x.root_id=a.root_id AND x.actor_type='user' AND x.actor_id=a.actor_id::text AND x.state='pending' AND x.released_at IS NULL AND x.submission_fence=jsonb_build_object('goal','upgrade','status','reserved_not_executable','authority',a.operation_id,'protected_digest',a.snapshot->>'protected_digest','observation',a.snapshot->'observation') FROM download_requirement x JOIN movie_upgrade_authority a USING(operation_id) WHERE x.id=$1`, id).Scan(&valid)
	default:
		return nil
	}
	if e != nil {
		return e
	}
	if !valid {
		return ports.ErrUpgradeReservationConflict
	}
	return nil
}
func upgradeValidateSuccess(ctx context.Context, tx pgx.Tx, table string, id uuid.UUID, a *ports.Actor, sub uuid.UUID, key, hash, action string, resource uuid.UUID, want json.RawMessage) error {
	var valid bool
	var e error
	switch table {
	case "audit_log":
		e = tx.QueryRow(ctx, `SELECT actor_type=$2 AND actor_id=$3 AND action=$4 AND target_type='subscription' AND target_id=$5 AND payload=$6::jsonb FROM audit_log WHERE id=$1`, id, string(a.Type), a.ID, action, sub.String(), want).Scan(&valid)
	case "outbox_event":
		e = tx.QueryRow(ctx, `SELECT aggregate_type='upgrade.command' AND aggregate_id=$2 AND event_type=$3 AND payload=$4::jsonb AND seq=1 FROM outbox_event WHERE id=$1`, id, resource.String(), action, want).Scan(&valid)
	case "idempotency_record":
		e = tx.QueryRow(ctx, `SELECT actor_type=$2 AND actor_id=$3 AND idempotency_key=$4 AND request_hash=$5 AND response_code=202 AND response_body=$6::jsonb AND resource_id=$7 FROM idempotency_record WHERE id=$1`, id, string(a.Type), a.ID, key, hash, want, resource.String()).Scan(&valid)
	default:
		return ports.ErrUpgradeReservationConflict
	}
	if e != nil {
		return e
	}
	if !valid {
		return ports.ErrUpgradeReservationConflict
	}
	return nil
}
