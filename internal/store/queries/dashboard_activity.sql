-- name: ListVisibleDashboardOperationSummaries :many
-- Homepage also includes automatic TV operations via their immutable owner.
-- This read projection does not grant or alter execution authority.
WITH visible AS (
 SELECT r.operation_id,min(r.created_at)::timestamptz AS created_at,min(m.title)::text AS title
 FROM download_requirement r
 JOIN media_unit u ON u.id=r.unit_id JOIN media m ON m.id=u.media_id
 WHERE (r.actor_type='user' AND (sqlc.arg(is_admin)::boolean OR r.actor_id=sqlc.arg(actor_id)::text))
 OR EXISTS(SELECT 1 FROM automatic_tv_execution_authority a
   WHERE a.operation_id=r.operation_id AND a.unit_id=r.unit_id AND a.root_id=r.root_id
   AND (sqlc.arg(is_admin)::boolean OR a.owner_id::text=sqlc.arg(actor_id)::text))
 GROUP BY r.operation_id
), projected AS (
 SELECT v.*, (CASE WHEN dt.id IS NOT NULL THEN
   CASE WHEN dt.state='completed' THEN CASE dt.organization_state
     WHEN 'completed' THEN 'completed' WHEN 'review_required' THEN 'review_required'
     WHEN 'queued' THEN 'organizing' ELSE 'waiting_organization' END ELSE dt.state END
   ELSE coalesce(p.state,CASE
     WHEN NOT EXISTS(SELECT 1 FROM download_requirement pending WHERE pending.operation_id=v.operation_id AND pending.state='pending') THEN 'cancelled'
     WHEN EXISTS(SELECT 1 FROM job submission WHERE submission.state IN ('queued','running') AND (
       (submission.kind IN ('download_submission','episode_download_submission') AND submission.payload->>'operation_id'=v.operation_id::text)
       OR (submission.kind='automatic_tv_download_submission' AND submission.dedupe_key=v.operation_id::text))) THEN 'queued'
     ELSE 'unknown' END) END)::text AS status
 FROM visible v LEFT JOIN download_task dt ON dt.operation_id=v.operation_id
 LEFT JOIN LATERAL(SELECT tp.state FROM transfer_plan tp WHERE tp.operation_id=v.operation_id
   AND (dt.transfer_plan_id IS NULL OR tp.id=dt.transfer_plan_id) ORDER BY tp.created_at DESC,tp.id DESC LIMIT 1) p ON true
)
SELECT * FROM projected WHERE status NOT IN ('completed','succeeded','cancelled')
ORDER BY created_at DESC,operation_id DESC LIMIT sqlc.arg(page_limit)::int;

-- name: ListDashboardActivityProgress :many
-- Only the already bounded, authorized operation IDs enter this batch. Select the
-- latest result before checking its state so review/failure cannot revive history.
SELECT dt.operation_id, dt.id AS task_id, dt.state, dt.content_path,
 dt.error, dt.progress, dt.total_bytes, dt.downloaded_bytes, dt.updated_at,
 EXISTS(SELECT 1 FROM automatic_tv_execution_authority a WHERE a.operation_id=dt.operation_id) AS automatic_tv,
 coalesce(latest.task_id,'00000000-0000-0000-0000-000000000000'::uuid) AS observation_task_id,
 coalesce(latest.state,'')::text AS observation_state,
 coalesce(latest.review_required,true)::boolean AS review_required,
 latest.created_at AS observation_created_at,
 coalesce((latest.statement::jsonb->'After')::text,'')::text AS verified_tasks
FROM download_task dt
LEFT JOIN LATERAL (
 SELECT r.task_id,r.state,r.review_required,r.created_at,r.statement
 FROM automatic_tv_observation_result r WHERE r.operation_id=dt.operation_id
 ORDER BY r.created_at DESC,r.job_id DESC LIMIT 1
) latest ON true
WHERE dt.operation_id=ANY(sqlc.arg(operation_ids)::uuid[]);
