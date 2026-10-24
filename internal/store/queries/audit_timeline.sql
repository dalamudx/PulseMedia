-- name: SetAuditTimelineReadOnly :exec
SET TRANSACTION READ ONLY;

-- name: ReadAuditTimeline :many
SELECT a.id, a.actor_type, a.actor_id, a.action, a.target_type, a.target_id, a.created_at
FROM audit_log a
WHERE (sqlc.arg(actor_type)::text = '' OR a.actor_type = sqlc.arg(actor_type))
  AND (sqlc.arg(actor_id)::text = '' OR a.actor_id = sqlc.arg(actor_id))
  AND (sqlc.arg(action)::text = '' OR a.action = sqlc.arg(action))
  AND (sqlc.arg(target_type)::text = '' OR a.target_type = sqlc.arg(target_type))
  AND (sqlc.arg(target_id)::text = '' OR a.target_id = sqlc.arg(target_id))
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR a.created_at >= sqlc.narg(from_time))
  AND (sqlc.narg(until_time)::timestamptz IS NULL OR a.created_at < sqlc.narg(until_time))
  AND (sqlc.narg(before_time)::timestamptz IS NULL OR (a.created_at, a.id) < (sqlc.narg(before_time), sqlc.narg(before_id)::uuid))
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg(page_limit)::integer;
