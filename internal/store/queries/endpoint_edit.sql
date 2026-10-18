-- Endpoint editing locks the current account before the configured endpoint.
-- These queries preserve existing background and integration update contracts.
-- name: LockIndexerForEdit :one
SELECT * FROM indexer WHERE id=$1 FOR UPDATE;

-- name: LockDownloadClientForEdit :one
SELECT * FROM download_client WHERE id=$1 FOR UPDATE;

-- name: LockMediaServerForEdit :one
SELECT * FROM media_server WHERE id=$1 FOR UPDATE;

-- name: EditIndexer :one
UPDATE indexer SET name=$2, url=$3,
 api_key_encrypted=COALESCE($4,api_key_encrypted),
 categories=COALESCE($5,categories), config_version=config_version+1,
 updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
WHERE id=$1 RETURNING *;

-- name: EditDownloadClient :one
UPDATE download_client SET name=$2,client_type=$3,url=$4,username=$5,
 password_encrypted=COALESCE($6,password_encrypted),
 updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
WHERE id=$1 RETURNING *;

-- name: EditMediaServer :one
UPDATE media_server SET name=$2,server_type=$3,url=$4,
 api_key_encrypted=COALESCE($5,api_key_encrypted),
 updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond')
WHERE id=$1 RETURNING *;
