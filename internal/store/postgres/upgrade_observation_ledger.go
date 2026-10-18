package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The original send ledger is checked separately. Each new action must have
// exactly one success audit/event/receipt, or none before its own writes.
func (s *movieUpgradeObservationSession) ledger(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub uuid.UUID, key, hash, action string, out any, existing bool) (ownedUpgradeRows, error) {
	return readUpgradeObservationLedger(ctx, tx, s.base.operation, a, sub, key, hash, action, out, existing)
}
func readUpgradeObservationLedger(ctx context.Context, tx pgx.Tx, operation uuid.UUID, a *ports.Actor, sub uuid.UUID, key, hash, action string, out any, existing bool) (ownedUpgradeRows, error) {
	own := ownedUpgradeRows{}
	for table, condition := range map[string]string{
		"audit_log":          "action=$2 AND payload->>'operation_id'=$1::text",
		"outbox_event":       "event_type=$2 AND aggregate_id=$1::text",
		"idempotency_record": "idempotency_key LIKE ($2||':%') AND resource_id=$1::text",
	} {
		rows, e := tx.Query(ctx, "SELECT id FROM "+table+" WHERE "+condition+" FOR SHARE", operation.String(), action)
		if e != nil {
			return nil, e
		}
		ids := []uuid.UUID{}
		for rows.Next() {
			var id uuid.UUID
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return nil, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if (!existing && len(ids) != 0) || (existing && len(ids) != 1) {
			return nil, preparationConflict()
		}
		for _, id := range ids {
			raw, e := marshalObservationReceipt(out)
			if e != nil {
				return nil, e
			}
			if e = upgradeValidateSuccess(ctx, tx, table, id, a, sub, action+":"+key, hash, action, operation, raw); e != nil {
				return nil, e
			}
			if e = own.remember(ctx, tx, table, id); e != nil {
				return nil, e
			}
		}
	}
	return own, nil
}
