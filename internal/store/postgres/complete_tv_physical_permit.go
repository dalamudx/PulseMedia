package postgres

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Standalone fact UoWs may have no native permit. A loaded native permit binds
// its exact owner and live original advisory session throughout this transaction.
func completeTVPhysicalPermit(ctx context.Context, tx pgx.Tx, operation uuid.UUID) (uuid.UUID, error) {
	var token uuid.UUID
	var pid int32
	var key int64
	err := tx.QueryRow(ctx, `SELECT token,backend_pid,advisory_key FROM complete_tv_physical_permit WHERE operation_id=$1 FOR SHARE`, operation).Scan(&token, &pid, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, err
	}
	var held bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=$1 AND granted AND mode='ExclusiveLock' AND objsubid=1 AND classid=(($2::bigint >> 32) & 4294967295)::oid AND objid=($2::bigint & 4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`, pid, key).Scan(&held)
	if err != nil {
		return uuid.Nil, err
	}
	if !held {
		return uuid.Nil, ports.ErrUnavailable
	}
	return token, nil
}
