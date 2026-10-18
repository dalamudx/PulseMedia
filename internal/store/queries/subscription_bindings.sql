-- name: ConfigureSubscriptionBindings :one
UPDATE subscription SET indexer_ids=$2,downloader_id=$3,quality_policy_id=sqlc.arg(policy_id),quality_policy_revision=sqlc.arg(policy_revision),intent_version=intent_version+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING *;
-- name: RefreshBindingWantedIntents :exec
UPDATE wanted_item SET intent_version=$2,next_evaluate_at=clock_timestamp(),updated_at=clock_timestamp() WHERE subscription_id=$1;
