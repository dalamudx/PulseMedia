-- name: GetCompleteTVOrganizationScope :one
SELECT a.actor_id,a.subscription_id,COALESCE(a.snapshot->>'policy_id','')::text AS policy_id
FROM complete_tv_execution_authority a JOIN subscription s ON s.id=a.subscription_id
WHERE a.operation_id=$1;

-- name: FindCompletedTVOrganizationTracking :one
SELECT id FROM job
WHERE kind='complete_tv_coverage_tracking' AND state='succeeded'
AND payload->>'operation_id'=sqlc.arg(operation_id)::text
AND payload->>'task_id'=sqlc.arg(task_id)::text
AND result->>'state'='completed'
ORDER BY created_at DESC,id DESC LIMIT 1;
