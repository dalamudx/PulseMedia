-- name: GetPersonFollowDispatch :one
SELECT r.id AS run_id,r.config_id,r.owner_id FROM person_follow_sync_page p JOIN person_follow_sync_run r ON r.id=p.run_id WHERE p.job_id=$1;

-- name: LockPersonFollowRun :one
SELECT * FROM person_follow_sync_run WHERE id=$1 FOR UPDATE;

-- name: LockPersonFollowPage :one
SELECT * FROM person_follow_sync_page WHERE job_id=$1 FOR UPDATE;

-- name: LockPersonFollowJob :one
SELECT * FROM job WHERE id=$1 FOR UPDATE;

-- name: TryPersonFollowPhysicalLock :one
SELECT pg_try_advisory_lock(pulse_person_follow_lock_key($1::uuid))::boolean;

-- name: UnlockPersonFollowPhysicalLock :one
SELECT pg_advisory_unlock(pulse_person_follow_lock_key($1::uuid))::boolean;

-- name: PersonFollowBackendPID :one
SELECT pg_backend_pid()::integer;

-- name: PersonFollowPhysicalLockHeld :one
SELECT pulse_person_follow_lock_held(pulse_person_follow_lock_key(sqlc.arg(config_id)::uuid),sqlc.arg(backend_pid)::int)::boolean;

-- name: PersonFollowUnreleasedExecution :one
SELECT EXISTS(SELECT 1 FROM person_follow_execution WHERE job_id=$1 AND released_at IS NULL);

-- name: InsertPersonFollowExecution :exec
INSERT INTO person_follow_execution(id,job_id,run_id,lease_owner,lease_token,backend_pid)VALUES($1,$2,$3,$4,$5,$6);

-- name: ReleasePersonFollowExecution :execrows
UPDATE person_follow_execution SET released_at=clock_timestamp() WHERE id=$1 AND released_at IS NULL;

-- name: ReadPersonFollowExecution :one
SELECT * FROM person_follow_execution WHERE id=$1;

-- name: StartPersonFollowRun :exec
UPDATE person_follow_sync_run SET status='running' WHERE id=$1 AND status='queued';

-- name: PersonFollowLeaseValid :one
SELECT kind='person_follow_sync' AND state='running' AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp() AS valid FROM job WHERE id=sqlc.arg(id)::uuid;
