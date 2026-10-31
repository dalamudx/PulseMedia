-- name: InsertExternalBatch :exec
INSERT INTO external_import_batch(id,owner_id,scope_id,command_key,input_hash,manifest,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: GetExternalBatch :one
SELECT manifest,input_hash FROM external_import_batch WHERE owner_id=$1 AND scope_id=$2 AND id=$3;

-- name: GetExternalBatchCommand :one
SELECT manifest,input_hash FROM external_import_batch WHERE owner_id=$1 AND scope_id=$2 AND command_key=$3;
