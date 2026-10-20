-- name: LockDoubanWishCreationJob :one
SELECT * FROM job WHERE id=$1 FOR UPDATE;

-- name: LockDoubanWishCreationConfig :one
SELECT * FROM douban_sync_config WHERE id=$1 FOR SHARE;

-- name: LockDoubanWishCreationRun :one
SELECT * FROM douban_wish_sync_run WHERE id=$1 FOR SHARE;

-- name: LockDoubanWishCreationPage :one
SELECT * FROM douban_wish_sync_page WHERE job_id=$1 FOR SHARE;

-- name: LockDoubanWishCreationItem :one
SELECT * FROM douban_wish_sync_item WHERE id=$1 FOR UPDATE;

-- name: DoubanWishCreationPermitHeld :one
SELECT pulse_douban_wish_permit_held(sqlc.arg(lock_key)::bigint,sqlc.arg(execution_pid)::int)::boolean;

-- name: DoubanWishCreationLeaseValid :one
SELECT EXISTS(SELECT 1 FROM job WHERE id=$1 AND kind='douban_wish_sync'
 AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp())::boolean;

-- name: RecordDoubanWishCreation :execrows
UPDATE douban_wish_sync_item SET state='created',subscription_id=$2,receipt=$3
WHERE id=$1 AND state='matched';

-- name: ListDoubanWishCreatedCommand :many
SELECT i.*,r.owner_id,r.config_id,r.config_version
FROM douban_wish_sync_item i JOIN douban_wish_sync_run r ON r.id=i.run_id
WHERE i.command_key=$1 AND i.state='created'
ORDER BY i.id;
