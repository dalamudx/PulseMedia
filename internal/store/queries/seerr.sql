-- name: LockSeerrConfigurationCommand :exec
SELECT pg_advisory_xact_lock(hashtextextended('seerr.configuration',0));

-- name: LockSeerrConfiguration :one
SELECT source_id,owner_id,configured_by,enabled,version,credential_encrypted
FROM seerr_configuration WHERE singleton FOR UPDATE;

-- name: SaveSeerrConfiguration :exec
INSERT INTO seerr_configuration(singleton,source_id,enabled,version,owner_id,configured_by,credential_encrypted)
VALUES(true,sqlc.arg(source_id)::uuid,sqlc.arg(enabled)::boolean,sqlc.arg(version)::bigint,sqlc.arg(owner_id)::uuid,sqlc.arg(configured_by)::uuid,sqlc.arg(credential_encrypted)::bytea)
ON CONFLICT(singleton) DO UPDATE SET enabled=EXCLUDED.enabled,version=EXCLUDED.version,configured_by=EXCLUDED.configured_by,credential_encrypted=EXCLUDED.credential_encrypted;

-- name: GetSeerrIngressReceipt :one
SELECT payload_hash,owner_id,request_id,config_version,created_at
FROM seerr_ingress_receipt WHERE source_id=sqlc.arg(source_id)::uuid AND external_request_id=sqlc.arg(external_request_id)::text;

-- name: InsertSeerrIngressReceipt :exec
INSERT INTO seerr_ingress_receipt(source_id,external_request_id,payload_hash,config_version,owner_id,request_id,created_at)
VALUES(sqlc.arg(source_id)::uuid,sqlc.arg(external_request_id)::text,sqlc.arg(payload_hash)::text,sqlc.arg(config_version)::bigint,sqlc.arg(owner_id)::uuid,sqlc.arg(request_id)::uuid,sqlc.arg(created_at)::timestamptz);

-- name: ListSeerrIngressReceipts :many
SELECT external_request_id,request_id,config_version,created_at FROM seerr_ingress_receipt
ORDER BY created_at DESC,external_request_id DESC LIMIT 50;
