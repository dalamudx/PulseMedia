-- name: AutomaticTVSubmissionAuthority :one
SELECT * FROM automatic_tv_execution_authority WHERE operation_id=$1;
-- name: AutomaticTVSubmissionPreparation :one
SELECT * FROM automatic_tv_submission_preparation WHERE operation_id=$1;
-- name: AutomaticTVSubmissionAttempt :one
SELECT * FROM automatic_tv_physical_attempt WHERE id=$1 FOR UPDATE;
-- name: AutomaticTVSubmissionJobValid :one
SELECT EXISTS(SELECT 1 FROM job WHERE id=sqlc.arg(job_id)::uuid AND kind='automatic_tv_download_submission'
 AND state='running' AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint
 AND lease_until>clock_timestamp() AND dedupe_key=sqlc.arg(operation_id)::uuid::text
 AND payload=sqlc.arg(payload)::jsonb) AS valid;
-- name: AutomaticTVInsertSubmissionPreparation :exec
INSERT INTO automatic_tv_submission_preparation(operation_id,task_id,physical_attempt_id,job_id,lease_owner,lease_token,authority_hash,info_hash,torrent_sha256,proof_hash,proof)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11);
-- name: AutomaticTVInsertSubmissionResult :exec
INSERT INTO automatic_tv_submission_result(operation_id,task_id,physical_attempt_id,job_id,lease_owner,lease_token,state) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: AutomaticTVSubmissionRequirements :many
SELECT * FROM download_requirement WHERE operation_id=$1 ORDER BY unit_id,root_id,id FOR UPDATE;
