-- name: ListActivityOperationSummaries :many
-- Owner visibility is read-only; it never grants command authority.
WITH visible AS (
 SELECT r.operation_id, min(r.created_at)::timestamptz AS created_at, min(m.title)::text AS title
 FROM download_requirement r
 JOIN media_unit u ON u.id=r.unit_id JOIN media m ON m.id=u.media_id
 WHERE (sqlc.arg(operation_filter)::uuid='00000000-0000-0000-0000-000000000000'::uuid OR r.operation_id=sqlc.arg(operation_filter)::uuid)
 AND ((r.actor_type='user' AND (sqlc.arg(is_admin)::boolean OR r.actor_id=sqlc.arg(actor_id)::text))
 OR EXISTS(SELECT 1 FROM automatic_tv_execution_authority a
   WHERE a.operation_id=r.operation_id AND a.unit_id=r.unit_id AND a.root_id=r.root_id
   AND (sqlc.arg(is_admin)::boolean OR a.owner_id::text=sqlc.arg(actor_id)::text)))
 GROUP BY r.operation_id
), projected AS (
 SELECT v.*, (CASE upgrade.outcome WHEN 'tracking' THEN 'tracking' WHEN 'completed' THEN 'completed' WHEN 'manual_review' THEN 'unknown' ELSE coalesce(dt.state,'') END)::text AS download_status, dt.progress,
 coalesce(p.id,'00000000-0000-0000-0000-000000000000'::uuid) AS plan_id,coalesce(p.state,'')::text AS plan_status,
 (CASE WHEN upgrade.outcome='tracking' THEN 'tracking'
 WHEN upgrade.outcome='manual_review' THEN 'review_required'
 WHEN upgrade.outcome='completed' THEN CASE
   WHEN p.state IN ('completed','succeeded') THEN 'completed'
   WHEN p.state IN ('partial','conflict','failed','review_required') THEN 'review_required'
   WHEN p.id IS NOT NULL THEN 'organizing' ELSE 'waiting_organization' END
 WHEN dt.id IS NOT NULL THEN CASE WHEN dt.state='completed' THEN CASE dt.organization_state
   WHEN 'completed' THEN 'completed' WHEN 'review_required' THEN 'review_required'
   WHEN 'queued' THEN 'organizing' ELSE 'waiting_organization' END ELSE dt.state END
 ELSE coalesce(p.state, CASE
   WHEN NOT EXISTS(SELECT 1 FROM download_requirement pending WHERE pending.operation_id=v.operation_id AND pending.state='pending') THEN 'cancelled'
   WHEN EXISTS(SELECT 1 FROM job submission WHERE submission.state IN ('queued','running') AND
     ((submission.kind IN ('download_submission','episode_download_submission') AND submission.payload->>'operation_id'=v.operation_id::text)
      OR (submission.kind='automatic_tv_download_submission' AND submission.dedupe_key=v.operation_id::text))) THEN 'queued'
   ELSE 'unknown' END) END)::text AS status
 FROM visible v LEFT JOIN download_task dt ON dt.operation_id=v.operation_id
 LEFT JOIN LATERAL (SELECT tp.id,tp.state FROM transfer_plan tp WHERE tp.operation_id=v.operation_id AND (dt.transfer_plan_id IS NULL OR tp.id=dt.transfer_plan_id) ORDER BY tp.created_at DESC,tp.id DESC LIMIT 1) p ON true
 LEFT JOIN LATERAL (SELECT result.outcome FROM movie_upgrade_tracking_observation observation LEFT JOIN movie_upgrade_tracking_result result USING(observation_id) WHERE observation.operation_id=v.operation_id AND NOT EXISTS(SELECT 1 FROM movie_upgrade_tracking_observation child WHERE child.prior_observation_id=observation.observation_id)) upgrade ON true
)
SELECT * FROM projected
WHERE (sqlc.arg(view)::text='all' OR (sqlc.arg(view)::text='active' AND status NOT IN ('completed','succeeded','cancelled')) OR (sqlc.arg(view)::text='review' AND status IN ('unknown','review_required','failed','dead','partial','conflict')))
 AND (NOT sqlc.arg(has_cursor)::boolean OR (created_at,operation_id)<(sqlc.arg(cursor_time)::timestamptz,sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC,operation_id DESC LIMIT sqlc.arg(page_limit)::int;
