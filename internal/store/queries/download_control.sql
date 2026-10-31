-- name: LockDownloadControlTask :one
SELECT t.* FROM download_task t WHERE t.operation_id=$1
 AND NOT EXISTS(SELECT 1 FROM download_task other WHERE other.client_id=t.client_id AND lower(other.info_hash)=lower(t.info_hash) AND other.id<>t.id AND other.state NOT IN ('failed','canceled'))
 FOR UPDATE OF t;

-- name: LockDownloadControlRequirements :many
SELECT * FROM download_requirement WHERE operation_id=$1 ORDER BY id FOR UPDATE;

-- name: GetDownloadControlCommand :one
SELECT * FROM download_control_command WHERE actor_id=$1 AND request_key=$2;

-- name: HasUnresolvedDownloadControl :one
SELECT EXISTS(SELECT 1 FROM download_control_command c WHERE c.task_id=$1 AND NOT EXISTS(SELECT 1 FROM download_control_confirmation f WHERE f.command_id=c.id))::boolean;

-- name: HasDownloadControlReturned :one
SELECT EXISTS(SELECT 1 FROM download_control_return WHERE command_id=$1)::boolean;

-- name: HasDownloadControlConfirmation :one
SELECT EXISTS(SELECT 1 FROM download_control_confirmation WHERE command_id=$1)::boolean;

-- name: CreateDownloadControlCommand :one
INSERT INTO download_control_command(id,task_id,operation_id,actor_id,request_key,action,affect_shared,client_id,client_stamp,info_hash,save_path,requirements,backend_pid,nonce,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,pg_backend_pid(),$13,clock_timestamp()+interval '60 seconds') RETURNING *;

-- name: CreateDownloadControlReturn :exec
INSERT INTO download_control_return(command_id) VALUES($1);

-- name: CreateDownloadControlConfirmation :exec
INSERT INTO download_control_confirmation(command_id,source) VALUES($1,$2) ON CONFLICT(command_id) DO NOTHING;

-- name: ValidateDownloadControlPermit :one
SELECT (c.backend_pid=pg_backend_pid() AND c.nonce=$2 AND c.expires_at>clock_timestamp() AND pulse_download_control_gate_held(c.task_id))::boolean FROM download_control_command c WHERE c.id=$1;

-- name: LockDownloadControlPhysical :one
SELECT pg_try_advisory_lock(hashtextextended('download-control:'||$1::uuid::text,0))::boolean;

-- name: UnlockDownloadControlPhysical :one
SELECT pg_advisory_unlock(hashtextextended('download-control:'||$1::uuid::text,0))::boolean;
