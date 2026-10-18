-- name: StoreSubscriptionSelectedUnits :exec
UPDATE subscription SET selected_unit_ids=$2 WHERE id=$1;
-- name: LockScopeMediaUnits :many
SELECT * FROM media_unit WHERE media_id=$1 ORDER BY id FOR NO KEY UPDATE;
-- name: ApplyScopeWanted :one
INSERT INTO wanted_item(id,subscription_id,unit_id,intent_version,is_skipped,user_skipped,scope_excluded,is_satisfied,next_evaluate_at) VALUES($1,$2,$3,$4,$5,false,$5,false,clock_timestamp())
ON CONFLICT(subscription_id,unit_id) DO UPDATE SET intent_version=EXCLUDED.intent_version,scope_excluded=EXCLUDED.scope_excluded,is_skipped=wanted_item.user_skipped OR EXCLUDED.scope_excluded,next_evaluate_at=clock_timestamp(),updated_at=clock_timestamp() RETURNING *;
-- name: SetUserWantedSkipped :one
UPDATE wanted_item SET user_skipped=$2,is_skipped=$2 OR scope_excluded,updated_at=clock_timestamp() WHERE subscription_id=$1 AND unit_id=$3 RETURNING *;
