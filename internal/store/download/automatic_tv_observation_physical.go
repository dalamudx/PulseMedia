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

// The persistent read permit survives session loss until this owner has awaited
// every native call and result writeback. It never finishes the sender attempt.
func (m *Manager) acquireAutomaticTVObservationPhysical(ctx context.Context, request ports.AutomaticTVObservationRequest) (*pgx.Conn, uuid.UUID, func(), error) {
	conn, err := pgx.ConnectConfig(ctx, m.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, uuid.Nil, nil, err
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
		return nil, uuid.Nil, nil, err
	}
	if !held {
		return nil, uuid.Nil, nil, ports.ErrAutomaticTVNeedsObservation
	}
	permit := m.idgen.NewUUID()
	var inserted uuid.UUID
	err = conn.QueryRow(ctx, `INSERT INTO automatic_tv_observation_permit
 (id,operation_id,task_id,job_id,lease_owner,lease_token,send_attempt_id,backend_pid,advisory_key)
 SELECT $1,a.operation_id,t.id,j.id,j.lease_owner,j.lease_token,p.physical_attempt_id,pg_backend_pid(),$7
 FROM job j
 JOIN automatic_tv_observation_job b ON b.job_id=j.id
 JOIN automatic_tv_execution_authority a ON a.operation_id=b.operation_id
 JOIN automatic_tv_submission_preparation p ON p.operation_id=a.operation_id AND p.task_id=b.task_id
 JOIN automatic_tv_physical_attempt x ON x.id=p.physical_attempt_id AND x.id=b.send_attempt_id
 JOIN download_task t ON t.id=p.task_id AND t.operation_id=a.operation_id
 WHERE a.operation_id=$2 AND t.id=$3 AND j.id=$4
 AND j.kind='automatic_tv_observation' AND j.dedupe_key=a.operation_id::text
 AND j.payload=jsonb_build_object('operation_id',a.operation_id,'task_id',t.id,'automatic_tv_authority',a.operation_id)
 AND j.state='running' AND j.lease_owner=$5 AND j.lease_token=$6 AND j.lease_until>clock_timestamp()
 AND t.candidate_id=a.candidate_id AND t.client_id=a.client_id AND t.info_hash=p.info_hash
 AND t.state IN ('submitting','unknown','tracking') AND p.authority_hash=a.decision_stamp
 AND x.operation_id=a.operation_id AND x.job_id=p.job_id AND x.lease_owner=p.lease_owner AND x.lease_token=p.lease_token
 AND x.finished_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM automatic_tv_physical_attempt active WHERE active.operation_id=a.operation_id AND active.finished_at IS NULL)
 ON CONFLICT DO NOTHING RETURNING id`, permit, request.OperationID, request.TaskID, request.Lease.ID, request.Lease.Owner, request.Lease.Token, key).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, nil, ports.ErrAutomaticTVNeedsObservation
	}
	if err != nil {
		return nil, uuid.Nil, nil, err
	}
	acquired = true
	return conn, inserted, func() {
		defer closeSession()
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Logical lease expiry does not prevent this nonce finishing its own I/O.
		// Failed cleanup retains the durable hold for manual review.
		_, _ = m.pool.Exec(cleanup, `UPDATE automatic_tv_observation_permit SET finished_at=clock_timestamp()
 WHERE id=$1 AND operation_id=$2 AND task_id=$3 AND job_id=$4 AND lease_owner=$5 AND lease_token=$6 AND finished_at IS NULL`, inserted, request.OperationID, request.TaskID, request.Lease.ID, request.Lease.Owner, request.Lease.Token)
	}, nil
}
