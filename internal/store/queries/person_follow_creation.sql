-- name: ListPersonFollowCreatedCommand :many
SELECT i.id,i.run_id,i.subscription_id,i.receipt,r.config_id,r.config_version,r.owner_id,i.tmdb_id,i.media_type,i.root_id FROM person_follow_sync_item i JOIN person_follow_sync_run r ON r.id=i.run_id WHERE i.command_key=$1 AND i.state='created' ORDER BY i.id;

-- name: RecordPersonFollowCreation :execrows
UPDATE person_follow_sync_item SET state='created',writer_execution_id=$2,subscription_id=$3,receipt=$4 WHERE id=$1 AND state='pending';
