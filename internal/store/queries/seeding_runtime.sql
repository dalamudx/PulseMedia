-- name: TrySeedingExecutionGate :one
SELECT pg_try_advisory_lock(hashtextextended('seeding-removal:'||sqlc.arg(client_id)::uuid::text,0))::boolean AS held;
-- name: ReleaseSeedingExecutionGate :one
SELECT pg_advisory_unlock(hashtextextended('seeding-removal:'||sqlc.arg(client_id)::uuid::text,0))::boolean AS released;
-- name: CheckSeedingExecutionGate :one
SELECT pulse_seeding_execution_gate_held(sqlc.arg(client_id)::uuid)::boolean AS held;
-- name: OpenSeedingExecution :one
INSERT INTO seeding_removal_execution(job_id,lease_token,lease_owner,client_id,backend_pid,observe_only) VALUES($1,$2,$3,$4,pg_backend_pid(),$5) RETURNING *;
-- name: GetSeedingExecution :one
SELECT * FROM seeding_removal_execution WHERE job_id=$1 AND lease_token=$2;
-- name: LatestSeedingExecution :one
SELECT * FROM seeding_removal_execution WHERE job_id=$1 ORDER BY lease_token DESC LIMIT 1;
-- name: UnreleasedSeedingExecution :one
SELECT * FROM seeding_removal_execution WHERE client_id=$1 AND released_at IS NULL;
-- name: ReserveSeedingExecutionRequest :one
UPDATE seeding_removal_execution SET requests=requests+1,started_at=COALESCE(started_at,clock_timestamp())
WHERE job_id=$1 AND lease_token=$2 AND lease_owner=$3 AND backend_pid=pg_backend_pid() AND released_at IS NULL AND result IS NULL AND requests<(CASE WHEN observe_only THEN 1 ELSE 3 END) RETURNING *;
-- name: ReleaseSeedingExecution :execrows
UPDATE seeding_removal_execution SET released_at=clock_timestamp() WHERE job_id=$1 AND lease_token=$2 AND lease_owner=$3 AND backend_pid=pg_backend_pid() AND released_at IS NULL;
-- name: StartSeedingRemovalSchedule :execrows
UPDATE seeding_removal_schedule SET state='running',updated_at=clock_timestamp() WHERE task_id=$1 AND active_job_id=$2 AND config_version=$3 AND state IN ('queued','running');
-- name: FinishSeedingExecution :execrows
UPDATE seeding_removal_execution SET result=$4,finished_at=clock_timestamp() WHERE job_id=$1 AND lease_token=$2 AND lease_owner=$3 AND backend_pid=pg_backend_pid() AND released_at IS NULL AND result IS NULL;
-- name: FinishSeedingRemovalJob :execrows
UPDATE job SET state=$4,result=$5,error=$6,not_before=clock_timestamp()+sqlc.arg(delay_seconds)::integer*interval '1 second',finished_at=CASE WHEN $4='queued' THEN NULL ELSE clock_timestamp() END,updated_at=clock_timestamp()
WHERE id=$1 AND kind='seeding_removal' AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp();
-- name: FinishSeedingRemovalSchedule :execrows
UPDATE seeding_removal_schedule SET state=$3,active_job_id=CASE WHEN $3='queued' THEN active_job_id ELSE NULL END,next_due_at=clock_timestamp()+sqlc.arg(interval_seconds)::integer*interval '1 second',updated_at=clock_timestamp()
WHERE task_id=$1 AND active_job_id=$2 AND state='running';
