-- name: InsertWebPushDevice :one
INSERT INTO web_push_device(id,user_id,vapid_version,endpoint_digest,subscription_encrypted)
VALUES($1,$2,$3,$4,$5) RETURNING id,status,created_at,updated_at;

-- name: CountActiveWebPushDevices :one
SELECT count(*) FROM web_push_device WHERE user_id=$1 AND status='active';

-- name: ListWebPushDevices :many
SELECT id,status,created_at,updated_at FROM web_push_device
WHERE user_id=$1 ORDER BY created_at DESC,id LIMIT 100;

-- name: LockWebPushDevice :one
SELECT id,user_id,status,created_at,updated_at FROM web_push_device WHERE id=$1 FOR UPDATE;

-- name: RevokeWebPushDevice :one
UPDATE web_push_device SET status='revoked',updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
WHERE id=$1 AND user_id=$2 AND status='active' AND updated_at=$3
RETURNING id,status,created_at,updated_at;
