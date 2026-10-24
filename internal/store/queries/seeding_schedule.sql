-- name: ListDueSeedingTasks :many
SELECT t.id AS task_id,t.operation_id,c.client_id,c.owner_id,c.version AS config_version,c.client_stamp
FROM download_task t JOIN download_task_client_binding b ON b.task_id=t.id
JOIN seeding_configuration c ON c.client_id=t.client_id AND c.client_stamp=b.client_stamp
JOIN app_user u ON u.id=c.owner_id
LEFT JOIN seeding_removal_schedule s ON s.task_id=t.id
CROSS JOIN LATERAL pulse_ordinary_seeding_protection(t.id,c.client_stamp) p
WHERE c.enabled AND u.is_active AND u.role='admin' AND t.state='completed' AND t.organization_state='completed'
AND (s.task_id IS NULL OR s.state='idle')
AND (CASE WHEN s.config_version=c.version THEN s.next_due_at ELSE GREATEST(c.updated_at,t.updated_at)+c.interval_seconds*interval '1 second' END)<=clock_timestamp()
AND NOT EXISTS(SELECT 1 FROM seeding_removal_permit x WHERE x.task_id=t.id)
AND p.owned AND p.imported AND NOT p.pending_work
ORDER BY COALESCE(s.next_due_at,GREATEST(c.updated_at,t.updated_at)),t.id LIMIT 64;

-- name: GetSeedingRemovalSchedule :one
SELECT * FROM seeding_removal_schedule WHERE task_id=$1 FOR UPDATE;

-- name: QueueSeedingRemovalSchedule :execrows
INSERT INTO seeding_removal_schedule(task_id,config_version,next_due_at,active_job_id,state)
VALUES($1,$2,clock_timestamp(),$3,'queued') ON CONFLICT(task_id) DO UPDATE SET config_version=EXCLUDED.config_version,active_job_id=EXCLUDED.active_job_id,state='queued',next_due_at=clock_timestamp(),updated_at=clock_timestamp() WHERE seeding_removal_schedule.state='idle';

-- name: CreateSeedingRemovalJob :one
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES($1,'seeding_removal',$2,'queued',$3,3,clock_timestamp(),clock_timestamp(),clock_timestamp()) RETURNING *;

-- name: CreateSeedingRemovalDispatch :exec
INSERT INTO seeding_removal_dispatch(job_id,task_id,client_id,client_stamp,config_version,owner_id) VALUES($1,$2,$3,$4,$5,$6);

-- name: GetSeedingRemovalDispatch :one
SELECT * FROM seeding_removal_dispatch WHERE job_id=$1;
