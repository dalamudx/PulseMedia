-- name: GetCompleteTVTrackingHint :one
SELECT * FROM complete_tv_tracking_hint WHERE job_id=$1;

-- name: GetCompleteTVTrackingHintReplay :one
SELECT * FROM complete_tv_tracking_hint WHERE operation_id=$1 AND hint_id=$2;

-- name: InsertCompleteTVTrackingHint :one
INSERT INTO complete_tv_tracking_hint(job_id,task_id,operation_id,prior_job_id,hint_id,instance_id,sequence,kind,relative_path,binding,source_device,source_inode,prior_digest,authority_digest,preparation_digest,protection_digest,request_digest)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING *;

-- name: GetCompleteTVTrackingHintPrior :one
SELECT * FROM job WHERE kind='complete_tv_coverage_tracking' AND payload->>'task_id'=$1::text
ORDER BY created_at DESC,id DESC LIMIT 1;
