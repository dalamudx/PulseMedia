package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type sendReturnLedgerWriter func(context.Context, string, string, *ports.MovieUpgradeSendResultReceipt, func() error) error

func (s *movieUpgradeSendSession) lockSendReturnLedger(ctx context.Context, tx pgx.Tx, a *ports.Actor, arm json.RawMessage) (sendReturnLedgerWriter, func(context.Context) error, error) {
	q := db.New(tx)
	if e := q.LockDownloadAcquisition(ctx, s.operation); e != nil {
		return nil, nil, e
	}
	var root, mediaID, sub uuid.UUID
	if e := tx.QueryRow(ctx, `SELECT root_id,(snapshot->'observation'->>'media_id')::UUID,subscription_id FROM movie_upgrade_authority WHERE operation_id=$1 FOR SHARE`, s.operation).Scan(&root, &mediaID, &sub); e != nil {
		return nil, nil, e
	}
	if _, e := q.LockAcquisitionRoot(ctx, root); e != nil {
		return nil, nil, e
	}
	if _, e := q.LockScopeMediaUnits(ctx, mediaID); e != nil {
		return nil, nil, e
	}
	// FOR UPDATE also bars new FK children until the original outcome commits.
	if _, e := tx.Exec(ctx, `SELECT u.id FROM media_unit u JOIN movie_upgrade_authority a ON a.unit_id=u.id WHERE a.operation_id=$1 FOR UPDATE OF u`, s.operation); e != nil {
		return nil, nil, e
	}
	for _, table := range []string{"acquisition_reservation", "unit_reservation", "download_requirement"} {
		if _, e := tx.Exec(ctx, "SELECT id FROM "+table+" WHERE operation_id=$1 FOR UPDATE", s.operation); e != nil {
			return nil, nil, e
		}
	}
	if _, e := tx.Exec(ctx, `SELECT p.operation_id FROM movie_upgrade_preparation p JOIN download_task t ON t.id=p.task_id JOIN movie_upgrade_send_authorization a ON a.operation_id=p.operation_id WHERE p.operation_id=$1 AND to_jsonb(a)=$2::jsonb FOR SHARE OF p,t,a`, s.operation, arm); e != nil {
		return nil, nil, e
	}
	if _, e := tx.Exec(ctx, `SELECT c.id FROM download_client c JOIN movie_upgrade_preparation p ON (p.task_snapshot->>'client_id')::uuid=c.id WHERE p.operation_id=$1 FOR SHARE OF c`, s.operation); e != nil {
		return nil, nil, e
	}
	if _, e := tx.Exec(ctx, `SELECT f.id FROM library_file f JOIN movie_upgrade_authority a ON a.old_file_id=f.id WHERE a.operation_id=$1 FOR UPDATE OF f`, s.operation); e != nil {
		return nil, nil, e
	}
	if _, e := tx.Exec(ctx, `SELECT m.file_id FROM library_file_unit m JOIN movie_upgrade_authority a ON a.old_file_id=m.file_id WHERE a.operation_id=$1 ORDER BY m.file_id,m.unit_id FOR UPDATE OF m`, s.operation); e != nil {
		return nil, nil, e
	}
	if _, e := tx.Exec(ctx, `SELECT f.id FROM unit_fact f JOIN movie_upgrade_authority a ON a.unit_id=f.unit_id WHERE a.operation_id=$1 FOR SHARE OF f`, s.operation); e != nil {
		return nil, nil, e
	}
	baseline, e := sendReturnCurrentDigest(ctx, tx, s.operation)
	if e != nil {
		return nil, nil, e
	}
	own := ownedUpgradeRows{}
	conditions := map[string]string{"audit_log": `action='upgrade.send-result' AND payload->>'operation_id'=$1`, "outbox_event": `event_type='upgrade.send-result' AND aggregate_id=$1`, "idempotency_record": `idempotency_key LIKE 'upgrade.send-result:%' AND resource_id=$1`}
	var old json.RawMessage
	e = tx.QueryRow(ctx, `SELECT to_jsonb(r) FROM movie_upgrade_send_result r WHERE operation_id=$1`, s.operation).Scan(&old)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, nil, e
	}
	if e == nil {
		var row movieUpgradeSendResultRow
		if json.Unmarshal(old, &row) != nil {
			return nil, nil, preparationConflict()
		}
		raw, e := json.Marshal(resultReceipt(row))
		if e != nil {
			return nil, nil, e
		}
		var hash string
		if e = tx.QueryRow(ctx, `SELECT encode(sha256(convert_to(($1::jsonb)::text,'UTF8')),'hex')`, old).Scan(&hash); e != nil {
			return nil, nil, e
		}
		for table, condition := range conditions {
			var ids []uuid.UUID
			rs, e := tx.Query(ctx, "SELECT id FROM "+table+" WHERE "+condition+" FOR SHARE", s.operation.String())
			if e != nil {
				return nil, nil, e
			}
			for rs.Next() {
				var id uuid.UUID
				if e = rs.Scan(&id); e != nil {
					rs.Close()
					return nil, nil, e
				}
				ids = append(ids, id)
			}
			e = rs.Err()
			rs.Close()
			if e != nil {
				return nil, nil, e
			}
			if len(ids) != 1 {
				return nil, nil, preparationConflict()
			}
			id := ids[0]
			if e = upgradeValidateSuccess(ctx, tx, table, id, a, sub, "upgrade.send-result:"+row.RequestKey, hash, "upgrade.send-result", s.operation, raw); e != nil {
				return nil, nil, e
			}
			if e = own.remember(ctx, tx, table, id); e != nil {
				return nil, nil, e
			}
		}
	}
	verify := func(c context.Context) error {
		for table, condition := range conditions {
			var n int
			if e := tx.QueryRow(c, "SELECT count(*) FROM "+table+" WHERE "+condition, s.operation.String()).Scan(&n); e != nil {
				return e
			}
			if n != len(own[table]) {
				return preparationConflict()
			}
		}
		current, e := sendReturnCurrentDigest(c, tx, s.operation)
		if e != nil {
			return e
		}
		if current != baseline {
			return preparationConflict()
		}
		return own.verify(c, tx)
	}
	write := func(c context.Context, key, hash string, out *ports.MovieUpgradeSendResultReceipt, check func() error) error {
		ledger := &UpgradeReservationStore{pool: s.store.pool, ids: s.store.ids}
		return ledger.upgradeSuccess(c, tx, a, sub, key, hash, "upgrade.send-result", s.operation, out, own, check)
	}
	return write, verify, nil
}
