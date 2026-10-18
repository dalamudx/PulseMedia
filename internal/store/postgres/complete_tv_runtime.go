package postgres

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CompleteTVRuntimeStore changes job ownership only. Preparation and physical
// permits are immutable inputs to scheduling, never reclaimed by this store.
type CompleteTVRuntimeStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewCompleteTVRuntimeStore(pool *pgxpool.Pool, ids ports.IDGen) *CompleteTVRuntimeStore {
	return &CompleteTVRuntimeStore{pool, ids}
}

var _ ports.CompleteTVRecoveryRepository = (*CompleteTVRuntimeStore)(nil)

func (s *CompleteTVRuntimeStore) RecoverCompleteTVJobs(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var held bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(624208171303)").Scan(&held); err != nil {
		return err
	}
	if !held {
		return nil
	}
	// Lock jobs before evaluating expiry against the current DB clock. No task or
	// business locks are taken here; worker UoWs retain their canonical lock order.
	_, err = tx.Exec(ctx, `WITH locked AS MATERIALIZED (
 SELECT j.* FROM job j WHERE j.kind IN ('complete_tv_coverage_download_submission','complete_tv_coverage_observation') AND j.state IN ('queued','running') ORDER BY j.id FOR UPDATE OF j SKIP LOCKED
 ) UPDATE job j SET
 state=CASE WHEN l.kind='complete_tv_coverage_observation' OR EXISTS(SELECT 1 FROM download_task t WHERE t.operation_id::text=l.payload->>'operation_id') OR EXISTS(SELECT 1 FROM complete_tv_submission_preparation p WHERE p.operation_id::text=l.payload->>'operation_id') OR l.attempts>=l.max_attempts THEN 'dead' ELSE 'queued' END,
 lease_owner=NULL,lease_until=NULL,lease_token=l.lease_token + 1,updated_at=clock_timestamp(),
 error=CASE WHEN l.kind='complete_tv_coverage_observation' THEN 'complete TV observation lease expired' ELSE 'complete TV submission fenced for recovery' END
 FROM locked l WHERE j.id=l.id AND (
 (l.state='running' AND l.lease_until<=clock_timestamp()) OR
 (l.state='queued' AND l.kind='complete_tv_coverage_download_submission' AND (EXISTS(SELECT 1 FROM download_task t WHERE t.operation_id::text=l.payload->>'operation_id') OR EXISTS(SELECT 1 FROM complete_tv_submission_preparation p WHERE p.operation_id::text=l.payload->>'operation_id'))))`)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT t.id,t.operation_id FROM download_task t
 JOIN complete_tv_execution_authority a ON a.operation_id=t.operation_id
 JOIN complete_tv_submission_preparation p ON p.operation_id=t.operation_id AND p.task_id=t.id
 WHERE t.state IN ('submitting','unknown')
 AND NOT EXISTS(SELECT 1 FROM complete_tv_physical_permit x WHERE x.operation_id=t.operation_id)
 AND NOT EXISTS(SELECT 1 FROM job j WHERE j.kind='complete_tv_coverage_download_submission' AND j.payload->>'operation_id'=t.operation_id::text AND (j.state='queued' OR (j.state='running' AND j.lease_until>clock_timestamp())))
 AND NOT EXISTS(SELECT 1 FROM job j WHERE j.kind='complete_tv_coverage_observation' AND j.payload->>'task_id'=t.id::text AND (j.state IN ('queued','running','succeeded','failed') OR (j.state='dead' AND j.error IS DISTINCT FROM 'complete TV observation lease expired')))
 AND (SELECT count(*) FROM job j WHERE j.kind='complete_tv_coverage_observation' AND j.payload->>'task_id'=t.id::text)<3
 ORDER BY t.id LIMIT 25`)
	if err != nil {
		return err
	}
	type work struct{ task, operation uuid.UUID }
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (work, error) { var w work; e := r.Scan(&w.task, &w.operation); return w, e })
	if err != nil {
		return err
	}
	for _, w := range items {
		// Existing partial unique index makes simultaneous schedulers dedupe atomically.
		_, err = tx.Exec(ctx, `INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts) VALUES($1,'complete_tv_coverage_observation',$2,'queued',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),3) ON CONFLICT(kind,dedupe_key) WHERE dedupe_key IS NOT NULL AND state IN ('queued','running') DO NOTHING`, s.ids.NewUUID(), "complete-tv-observe:"+w.task.String(), w.task.String(), w.operation.String())
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
