-- name: AutomaticTVObservationLeaseValid :one
SELECT EXISTS(SELECT 1 FROM job WHERE id=sqlc.arg(job_id)::uuid AND kind='automatic_tv_observation'
 AND state='running' AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint
 AND lease_until>clock_timestamp() AND dedupe_key=sqlc.arg(operation_id)::uuid::text
 AND payload=sqlc.arg(payload)::jsonb) AS valid;

-- name: AutomaticTVObservationLink :one
SELECT * FROM automatic_tv_observation_job WHERE job_id=$1;

-- name: AutomaticTVObservationReadResult :one
SELECT * FROM automatic_tv_observation_result WHERE job_id=$1;

-- name: AutomaticTVObservationReadPermit :one
SELECT * FROM automatic_tv_observation_permit WHERE id=$1 FOR UPDATE;

-- name: AutomaticTVObservationInsertJob :exec
INSERT INTO automatic_tv_observation_job(job_id,operation_id,task_id,send_attempt_id) VALUES($1,$2,$3,$4);
