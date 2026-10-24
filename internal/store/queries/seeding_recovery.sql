-- name: ListRecoverableSeedingJobs :many
SELECT d.job_id,d.task_id,d.client_id,d.owner_id,d.client_stamp,d.config_version
FROM seeding_removal_dispatch d JOIN seeding_removal_schedule s ON s.active_job_id=d.job_id AND s.task_id=d.task_id
JOIN job j ON j.id=d.job_id
JOIN seeding_configuration c ON c.client_id=d.client_id
JOIN app_user u ON u.id=d.owner_id
WHERE s.state IN ('queued','running') AND (
 (j.state='running' AND j.lease_until<=clock_timestamp()) OR j.state IN ('dead','succeeded')
 OR (j.state='queued' AND (j.attempts>=j.max_attempts OR NOT u.is_active OR u.role<>'admin' OR c.owner_id<>d.owner_id OR c.client_stamp<>d.client_stamp
 OR pulse_global_rates_binding(d.client_id) IS DISTINCT FROM d.client_stamp
 OR ((NOT c.enabled OR c.version<>d.config_version) AND NOT EXISTS(SELECT 1 FROM seeding_removal_permit p WHERE p.task_id=d.task_id))))
)
ORDER BY s.updated_at,d.task_id LIMIT 64;
-- name: TrySeedingRecoveryGate :one
SELECT pg_try_advisory_xact_lock(hashtextextended('seeding-removal:'||sqlc.arg(client_id)::uuid::text,0))::boolean AS held;
-- name: TouchSeedingRecoverySchedule :execrows
UPDATE seeding_removal_schedule SET updated_at=clock_timestamp() WHERE task_id=$1 AND active_job_id=$2 AND state IN ('queued','running');
-- name: RecoverSeedingRemovalJob :execrows
UPDATE job SET state=sqlc.arg(next_state),lease_owner=NULL,lease_until=NULL,
 not_before=clock_timestamp(),updated_at=clock_timestamp(),finished_at=CASE WHEN sqlc.arg(next_state)::text='dead' THEN clock_timestamp() ELSE NULL END,
 error=sqlc.arg(reason)::text
WHERE id=sqlc.arg(job_id) AND kind='seeding_removal' AND state=sqlc.arg(previous_state) AND lease_token=sqlc.arg(lease_token)
AND (state<>'running' OR lease_until<=clock_timestamp());
-- name: RecoverSeedingRemovalSchedule :execrows
UPDATE seeding_removal_schedule SET state=sqlc.arg(next_state),active_job_id=CASE WHEN sqlc.arg(next_state)::text='queued' THEN active_job_id ELSE NULL END,
 next_due_at=clock_timestamp()+sqlc.arg(interval_seconds)::integer*interval '1 second',updated_at=clock_timestamp()
WHERE task_id=sqlc.arg(task_id) AND active_job_id=sqlc.arg(job_id) AND state IN ('queued','running');
