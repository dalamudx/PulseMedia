-- Transfer Queries

-- name: LockTransferRoot :one
SELECT * FROM library_root WHERE id = $1 FOR UPDATE;

-- name: LockTransferUnit :one
SELECT * FROM media_unit WHERE id = $1 FOR UPDATE;

-- name: LockTransferOldFile :one
SELECT * FROM library_file WHERE id = $1 FOR UPDATE;

-- name: ListTransferSourceRoots :many
SELECT path FROM library_root WHERE health = 'online'
UNION
SELECT mapping.value AS path
FROM download_task t JOIN download_client c ON c.id = t.client_id
CROSS JOIN LATERAL jsonb_each_text(CASE WHEN jsonb_typeof(c.remote_path_map) = 'object'
    THEN c.remote_path_map ELSE '{}'::jsonb END) AS mapping
WHERE t.operation_id = $1 AND t.state IN ('tracking', 'completed');

-- name: FreezeTransferPlan :exec
UPDATE transfer_plan SET root_path = $2, root_generation = $3, old_file_id = $4, root_device = $5, root_inode = $6 WHERE id = $1;

-- name: FreezeTransferStep :exec
UPDATE transfer_step SET source_root = $2, source_rel = $3, target_rel = $4,
source_device = $5, source_inode = $6, quality_profile = $7, target_evidence = $8 WHERE id = $1;

-- name: ClaimTransferPlan :one
WITH locked AS MATERIALIZED (SELECT p.* FROM transfer_plan p WHERE p.id=sqlc.arg(id) FOR UPDATE)
UPDATE transfer_plan t SET state='executing',execution_owner=sqlc.arg(owner),execution_token=t.execution_token+1,lease_until=clock_timestamp()+interval '2 minutes',updated_at=clock_timestamp()
FROM locked WHERE t.id=locked.id AND (locked.state='approved' OR (locked.state='executing' AND locked.lease_until<=clock_timestamp())) RETURNING t.*;

-- name: GuardTransferPlan :one
WITH locked AS MATERIALIZED (SELECT p.* FROM transfer_plan p WHERE p.id=sqlc.arg(id) FOR UPDATE)
UPDATE transfer_plan t SET lease_until=clock_timestamp()+interval '2 minutes'
FROM locked WHERE t.id=locked.id AND locked.state='executing' AND locked.execution_owner=sqlc.arg(owner)::text AND locked.execution_token=sqlc.arg(token)::bigint AND locked.lease_until>clock_timestamp() RETURNING t.*;

-- name: FinishTransferPlan :one
WITH locked AS MATERIALIZED (SELECT p.* FROM transfer_plan p WHERE p.id=sqlc.arg(id) FOR UPDATE)
UPDATE transfer_plan t SET state='completed',finished_at=clock_timestamp(),updated_at=clock_timestamp()
FROM locked WHERE t.id=locked.id AND locked.state='executing' AND locked.execution_owner=sqlc.arg(owner)::text AND locked.execution_token=sqlc.arg(token)::bigint AND locked.lease_until>clock_timestamp()
AND NOT EXISTS(SELECT 1 FROM transfer_step ts WHERE ts.plan_id=t.id AND ts.state<>'completed') RETURNING t.*;

-- name: ReviewTransferPlan :one
WITH locked AS MATERIALIZED (SELECT p.* FROM transfer_plan p WHERE p.id=sqlc.arg(id) FOR UPDATE)
UPDATE transfer_plan t SET state='review_required',updated_at=clock_timestamp()
FROM locked WHERE t.id=locked.id AND locked.state='executing' AND locked.execution_owner=sqlc.arg(owner)::text AND locked.execution_token=sqlc.arg(token)::bigint AND locked.lease_until>clock_timestamp() RETURNING t.*;

-- name: ConsumeTransferReservation :exec
UPDATE unit_reservation SET state = 'consumed', updated_at = statement_timestamp()
WHERE operation_id = $1 AND unit_id = $2 AND root_id = $3 AND state IN ('reserved', 'dispatched');


-- name: CreateTransferPlan :one
INSERT INTO transfer_plan (
    id, operation_id, root_id, state, requires_approval, strategy, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetTransferPlanByID :one
SELECT * FROM transfer_plan WHERE id = $1;

-- name: GetTransferPlanByOpID :one
SELECT p.* FROM transfer_plan p LEFT JOIN download_task t ON t.operation_id=p.operation_id
WHERE p.operation_id=$1 ORDER BY (p.id=t.transfer_plan_id) DESC NULLS LAST,p.created_at DESC,p.id DESC LIMIT 1;

-- name: UpdatePlanState :one
UPDATE transfer_plan
SET state = $2, updated_at = statement_timestamp()
WHERE id = $1 AND state NOT IN ('executing', 'completed') AND $2 IN ('approved', 'review_required', 'failed')
RETURNING *;

-- name: CreateTransferStep :one
INSERT INTO transfer_step (
    id, plan_id, unit_id, source_path, target_path, action, state,
    step_order, source_size, source_mtime, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: ListStepsForPlan :many
SELECT * FROM transfer_step
WHERE plan_id = $1
ORDER BY step_order ASC;

-- name: UpdateStepRunning :one
UPDATE transfer_step
SET state = 'running', updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: CompleteStep :one
UPDATE transfer_step
SET state = 'completed', target_file_id = $2, finished_at = statement_timestamp(), updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;


