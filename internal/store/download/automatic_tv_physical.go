package download

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A crashed owner leaves an active attempt for observation/manual review. Neither
// logical lease expiry nor a lost backend proves that the physical Add returned.
func (m *Manager) acquireAutomaticTVPhysical(ctx context.Context, request ports.AutomaticTVSendRequest) (uuid.UUID, func(), error) {
	conn, err := pgx.ConnectConfig(ctx, m.pool.Config().ConnConfig.Copy())
	if err != nil {
		return uuid.Nil, nil, err
	}
	closeSession := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
	}
	acquired := false
	defer func() {
		if !acquired {
			closeSession()
		}
	}()
	digest := sha256.Sum256([]byte("pulsemedia.automatic-tv.physical.v1:" + request.OperationID.String()))
	key := int64(binary.BigEndian.Uint64(digest[:8]))
	var held bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, key).Scan(&held); err != nil {
		return uuid.Nil, nil, err
	}
	if !held {
		return uuid.Nil, nil, ports.ErrAutomaticTVNeedsObservation
	}
	attempt := m.idgen.NewUUID()
	var inserted uuid.UUID
	err = conn.QueryRow(ctx, `INSERT INTO automatic_tv_physical_attempt
 (id,operation_id,job_id,lease_owner,lease_token,backend_pid,advisory_key)
 SELECT $1,a.operation_id,j.id,j.lease_owner,j.lease_token,pg_backend_pid(),$6
 FROM job j JOIN automatic_tv_execution_authority a ON a.operation_id=$2
 WHERE j.id=$3 AND j.kind='automatic_tv_download_submission'
 AND j.state='running' AND j.lease_owner=$4 AND j.lease_token=$5
 AND j.lease_until>clock_timestamp() AND j.dedupe_key=a.operation_id::text
 AND j.payload=jsonb_build_object('operation_id',a.operation_id,'round_id',a.round_id,
 'automatic_tv_authority',a.operation_id,'candidate_id',a.candidate_id,'root_id',a.root_id,'client_id',a.client_id)
 ON CONFLICT DO NOTHING RETURNING id`, attempt, request.OperationID, request.Lease.ID, request.Lease.Owner, request.Lease.Token, key).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, ports.ErrAutomaticTVNeedsObservation
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	acquired = true
	return inserted, func() {
		defer closeSession()
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Completion may outlive the original job lease. Only this immutable nonce can
		// mark the physical call finished, after synchronous I/O and result writeback.
		_, _ = m.pool.Exec(cleanup, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp()
 WHERE id=$1 AND operation_id=$2 AND job_id=$3 AND lease_owner=$4 AND lease_token=$5 AND finished_at IS NULL`, inserted, request.OperationID, request.Lease.ID, request.Lease.Owner, request.Lease.Token)
	}, nil
}
