-- name: ListVisibleOperationSummaries :many
WITH visible AS (
 SELECT r.operation_id, min(r.created_at)::timestamptz AS created_at, min(m.title)::text AS title
 FROM download_requirement r
 JOIN media_unit u ON u.id=r.unit_id JOIN media m ON m.id=u.media_id
 WHERE r.actor_type='user' AND (sqlc.arg(is_admin)::boolean OR r.actor_id=sqlc.arg(actor_id)::text)
 GROUP BY r.operation_id
), projected AS (
 SELECT v.*, coalesce(dt.state,'')::text AS download_status, dt.progress,
 coalesce(p.id,'00000000-0000-0000-0000-000000000000'::uuid) AS plan_id,coalesce(p.state,'')::text AS plan_status,
 (CASE WHEN dt.id IS NOT NULL THEN CASE WHEN dt.state='completed' THEN CASE dt.organization_state WHEN 'completed' THEN 'completed' WHEN 'review_required' THEN 'review_required' WHEN 'queued' THEN 'organizing' ELSE 'waiting_organization' END ELSE dt.state END ELSE coalesce(p.state, CASE WHEN NOT EXISTS(SELECT 1 FROM download_requirement pending WHERE pending.operation_id=v.operation_id AND pending.state='pending') THEN 'cancelled' WHEN EXISTS(SELECT 1 FROM job submission WHERE submission.kind IN ('download_submission','episode_download_submission') AND submission.payload->>'operation_id'=v.operation_id::text AND submission.state IN ('queued','running')) THEN 'queued' ELSE 'unknown' END) END)::text AS status
 FROM visible v LEFT JOIN download_task dt ON dt.operation_id=v.operation_id
 LEFT JOIN LATERAL (SELECT tp.id,tp.state FROM transfer_plan tp WHERE tp.operation_id=v.operation_id AND (dt.transfer_plan_id IS NULL OR tp.id=dt.transfer_plan_id) ORDER BY tp.created_at DESC,tp.id DESC LIMIT 1) p ON true
)
SELECT * FROM projected
WHERE (sqlc.arg(view)::text='all' OR (sqlc.arg(view)::text='active' AND status NOT IN ('completed','succeeded','cancelled')) OR (sqlc.arg(view)::text='review' AND status IN ('unknown','review_required','failed','dead','partial','conflict')))
 AND (NOT sqlc.arg(has_cursor)::boolean OR (created_at,operation_id)<(sqlc.arg(cursor_time)::timestamptz,sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC,operation_id DESC LIMIT sqlc.arg(page_limit)::int;
-- name: HasQueuedOperationSubmission :one
SELECT EXISTS(SELECT 1 FROM job WHERE kind IN ('download_submission','episode_download_submission') AND payload->>'operation_id'=sqlc.arg(operation_id)::text AND state IN ('queued','running'))::boolean;

