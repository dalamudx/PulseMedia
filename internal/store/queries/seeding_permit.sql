-- name: GetSeedingRemovalPermit :one
SELECT * FROM seeding_removal_permit WHERE task_id=sqlc.arg(task_id);

-- name: CreateSeedingRemovalPermit :one
INSERT INTO seeding_removal_permit(
 task_id,operation_id,client_id,client_stamp,config_version,owner_id,job_id,lease_owner,lease_token,
 info_hash,operation_tag,save_path,total_bytes,complete,seeding_seconds,share_ratio,infinite_share_ratio,free_bytes,free_space_path,observed_at
) VALUES(
 sqlc.arg(task_id),sqlc.arg(operation_id),sqlc.arg(client_id),sqlc.arg(client_stamp),sqlc.arg(config_version),sqlc.arg(owner_id),sqlc.arg(job_id),sqlc.arg(lease_owner),sqlc.arg(lease_token),
 sqlc.arg(info_hash),sqlc.arg(operation_tag),sqlc.arg(save_path),sqlc.arg(total_bytes),TRUE,
 sqlc.narg(seeding_seconds)::bigint,sqlc.narg(share_ratio)::double precision,sqlc.arg(infinite_share_ratio)::boolean,
 sqlc.narg(free_bytes)::bigint,sqlc.narg(free_space_path)::text,sqlc.arg(observed_at)::timestamptz
) RETURNING *;
