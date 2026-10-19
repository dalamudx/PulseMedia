-- name: OperationTimelineVisibility :one
SELECT EXISTS(SELECT 1 FROM download_requirement r
 WHERE r.operation_id=sqlc.arg(operation_id)::uuid)::boolean AS operation_exists,
 EXISTS(SELECT 1 FROM download_requirement r
 WHERE r.operation_id=sqlc.arg(operation_id)::uuid
 AND ((r.actor_type='user' AND (sqlc.arg(is_admin)::boolean OR r.actor_id=sqlc.arg(actor_id)::text))
 OR EXISTS(SELECT 1 FROM automatic_tv_execution_authority a
 WHERE a.operation_id=r.operation_id AND a.unit_id=r.unit_id AND a.root_id=r.root_id
 AND (sqlc.arg(is_admin)::boolean OR a.owner_id::text=sqlc.arg(actor_id)::text))))::boolean AS visible;

-- name: ListOperationTimelineSteps :many
SELECT id,step_order,action,state,target_rel,source_rel,updated_at,finished_at
FROM transfer_step
WHERE plan_id=sqlc.arg(plan_id)::uuid
AND (NOT sqlc.arg(has_cursor)::boolean OR (step_order,id)>(sqlc.arg(cursor_order)::integer,sqlc.arg(cursor_id)::uuid))
ORDER BY step_order ASC,id ASC
LIMIT sqlc.arg(page_limit)::integer;

-- name: OperationTimelineSnapshotTime :one
SELECT transaction_timestamp()::timestamptz AS observed_at;
