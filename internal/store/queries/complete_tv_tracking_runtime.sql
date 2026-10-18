-- name: LockCompleteTVTrackingScheduler :one
SELECT pg_try_advisory_xact_lock(624208171304)::boolean AS held;

-- name: ExpireCompleteTVTrackingJobs :exec
WITH locked AS MATERIALIZED (
 SELECT j.* FROM job j
 WHERE j.kind='complete_tv_coverage_tracking' AND j.state='running'
 ORDER BY j.id FOR UPDATE OF j SKIP LOCKED
)
UPDATE job j SET state='dead', lease_owner=NULL, lease_until=NULL,
 lease_token=l.lease_token + 1, updated_at=clock_timestamp(),
 error='complete TV tracking lease expired'
FROM locked l WHERE j.id=l.id AND l.lease_until<=clock_timestamp();

-- name: ListDueCompleteTVTracking :many
SELECT t.id AS task_id,t.operation_id
FROM download_task t
JOIN complete_tv_execution_authority a ON a.operation_id=t.operation_id
JOIN complete_tv_submission_preparation p ON p.operation_id=t.operation_id AND p.task_id=t.id
LEFT JOIN LATERAL (
 SELECT j.* FROM job j WHERE j.kind='complete_tv_coverage_tracking'
 AND j.payload->>'task_id'=t.id::text ORDER BY j.created_at DESC,j.id DESC LIMIT 1
) last_job ON true
WHERE t.state='tracking' AND p.proof->>'kind'='complete_tv_prepared_v2'
AND NOT EXISTS(SELECT 1 FROM complete_tv_physical_permit x WHERE x.operation_id=t.operation_id)
AND NOT EXISTS(SELECT 1 FROM job j WHERE j.payload->>'operation_id'=t.operation_id::text
 AND j.kind IN ('complete_tv_coverage_download_submission','complete_tv_coverage_observation','complete_tv_coverage_tracking')
 AND j.state IN ('queued','running'))
AND ((last_job.id IS NULL AND t.updated_at<=clock_timestamp()-interval '30 seconds') OR (
 last_job.updated_at <= clock_timestamp()-interval '30 seconds'
 AND ((last_job.state='succeeded' AND last_job.result->>'state'='tracking')
 OR (last_job.state='dead' AND last_job.error='complete TV tracking lease expired'
 AND (SELECT count(*) FROM job j WHERE j.kind='complete_tv_coverage_tracking'
  AND j.payload->>'task_id'=t.id::text AND j.state='dead'
  AND j.created_at>COALESCE((SELECT max(s.created_at) FROM job s
    WHERE s.kind='complete_tv_coverage_tracking' AND s.payload->>'task_id'=t.id::text
    AND s.state='succeeded' AND s.result->>'state'='tracking'),'-infinity'::timestamptz))<3))))
ORDER BY t.id LIMIT 25 FOR UPDATE OF t SKIP LOCKED;

-- name: EnqueueCompleteTVTracking :exec
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts)
VALUES(sqlc.arg(id)::uuid,'complete_tv_coverage_tracking',sqlc.arg(dedupe_key)::text,'queued',
 jsonb_build_object('task_id',sqlc.arg(task_id)::text,'operation_id',sqlc.arg(operation_id)::text),3)
ON CONFLICT(kind,dedupe_key) WHERE dedupe_key IS NOT NULL AND state IN ('queued','running') DO NOTHING;
