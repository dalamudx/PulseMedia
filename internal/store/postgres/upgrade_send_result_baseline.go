package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A return may follow config/fact changes during I/O. Freeze their CURRENT values
// only for this UoW so the result/ledger cannot itself mutate existing business state.
func sendReturnCurrentDigest(ctx context.Context, tx pgx.Tx, operation uuid.UUID) (string, error) {
	var digest string
	err := tx.QueryRow(ctx, `SELECT encode(sha256(convert_to(jsonb_build_object(
 'root',(SELECT to_jsonb(r) FROM library_root r WHERE r.id=a.root_id),
 'client',(SELECT to_jsonb(c) FROM download_client c WHERE c.id=(t.task_snapshot->>'client_id')::uuid),
 'subscription',(SELECT to_jsonb(s) FROM subscription s WHERE s.id=a.subscription_id),
 'unit',(SELECT to_jsonb(u) FROM media_unit u WHERE u.id=a.unit_id),
 'file',(SELECT to_jsonb(f) FROM library_file f WHERE f.id=a.old_file_id),
 'facts',(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM unit_fact f WHERE f.unit_id=a.unit_id),
 'mappings',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.file_id,m.unit_id) FROM library_file_unit m WHERE m.file_id=a.old_file_id),
 'lane',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM acquisition_reservation x WHERE x.operation_id=a.operation_id),
 'reservation',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_reservation x WHERE x.operation_id=a.operation_id),
 'requirement',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x WHERE x.operation_id=a.operation_id)
 )::text,'UTF8')),'hex') FROM movie_upgrade_authority a JOIN movie_upgrade_preparation t ON t.operation_id=a.operation_id WHERE a.operation_id=$1`, operation).Scan(&digest)
	return digest, err
}
