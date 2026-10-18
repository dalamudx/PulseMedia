-- name: GetAuthorizedCompleteTVPreparationJob :one
SELECT * FROM job WHERE id=sqlc.arg(id) AND kind='complete_tv_coverage_download_submission' AND state='running' AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp();

-- name: ListCompleteTVPreparationLanes :many
SELECT * FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL ORDER BY unit_id FOR UPDATE;

-- name: ListCompleteTVPreparationReservations :many
SELECT * FROM unit_reservation WHERE operation_id=$1 AND state IN ('reserved','dispatched') ORDER BY unit_id,root_id FOR UPDATE;

-- name: GetCompleteTVPreparation :one
SELECT * FROM complete_tv_submission_preparation WHERE operation_id=$1;

-- name: CreateCompleteTVPreparation :exec
INSERT INTO complete_tv_submission_preparation(operation_id,task_id,job_id,lease_owner,lease_token,info_hash,torrent_sha256,proof) VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: CreateCompleteTVPreparedUnit :exec
INSERT INTO complete_tv_submission_unit(task_id,unit_id,unit_version,provider_episode_id,unit_key,video_path,video_size) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: ListCompleteTVPreparedUnits :many
SELECT * FROM complete_tv_submission_unit WHERE task_id=$1 ORDER BY unit_id;

-- name: DispatchCompleteTVReservations :execrows
UPDATE unit_reservation SET state='dispatched',updated_at=clock_timestamp() WHERE operation_id=$1 AND state='reserved' AND expires_at>clock_timestamp();

