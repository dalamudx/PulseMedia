-- name: LockDoubanSyncOwner :one
SELECT role,is_active FROM app_user WHERE id=$1 FOR UPDATE;

-- name: GuardDoubanSyncIsolation :one
SELECT isolated FROM restore_isolation WHERE singleton FOR SHARE;

-- name: GetDoubanSyncConfig :one
SELECT * FROM douban_sync_config WHERE owner_id=$1 FOR SHARE;

-- name: LockDoubanSyncRoots :many
SELECT id,media_type,health FROM library_root WHERE id=ANY($1::uuid[]) ORDER BY id FOR SHARE;

-- name: GetDoubanSyncReceipt :one
SELECT * FROM douban_sync_config_receipt WHERE owner_id=$1 AND command_key=$2;

-- name: CreateDoubanSyncConfig :one
INSERT INTO douban_sync_config(id,owner_id,douban_uid,enabled,movie_root_id,tv_root_id,version)
VALUES($1,$2,$3,$4,$5,$6,1) RETURNING *;

-- name: UpdateDoubanSyncConfig :one
UPDATE douban_sync_config SET douban_uid=$2,enabled=$3,movie_root_id=$4,tv_root_id=$5,version=version+1,updated_at=clock_timestamp()
WHERE owner_id=$1 RETURNING *;

-- name: CreateDoubanSyncReceipt :exec
INSERT INTO douban_sync_config_receipt(owner_id,command_key,config_id,version,request_hash,input,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: AuditDoubanSyncConfig :exec
INSERT INTO audit_log(id,actor_type,actor_id,action,target_type,target_id,payload)
VALUES($1,'user',$2,'douban.sync.config.saved','douban_sync_config',$3,$4);

-- name: PublishDoubanSyncConfig :exec
INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq)
VALUES($1,'douban_sync_config',$2,'douban.sync.config.saved',$3,$4);
