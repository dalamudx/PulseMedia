-- name: GetUntrackedScopeRow :one
SELECT to_jsonb(s)::jsonb AS document FROM untracked_directory_scope s WHERE id=$1;

-- name: LockUntrackedScopeRow :one
SELECT to_jsonb(s)::jsonb AS document FROM untracked_directory_scope s WHERE id=$1 FOR UPDATE;

-- name: GetUntrackedScopeReceiptRow :one
SELECT to_jsonb(r)::jsonb AS document FROM untracked_directory_scope_receipt r WHERE scope_id=$1 AND command_key=$2;

-- name: GetUntrackedScopeReceiptVersionRow :one
SELECT to_jsonb(r)::jsonb AS document FROM untracked_directory_scope_receipt r WHERE scope_id=$1 AND version=$2;

-- name: InsertUntrackedScope :exec
INSERT INTO untracked_directory_scope(id,owner_id,enabled,version,client_id,target_root_id,local_root,snapshot,created_at,updated_at)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(enabled)::boolean,sqlc.arg(version)::bigint,sqlc.arg(client_id)::uuid,sqlc.arg(target_root_id)::uuid,sqlc.arg(local_root)::text,sqlc.arg(snapshot)::jsonb,sqlc.arg(created_at)::timestamptz,sqlc.arg(updated_at)::timestamptz);

-- name: UpdateUntrackedScope :execrows
UPDATE untracked_directory_scope SET enabled=sqlc.arg(enabled)::boolean,version=version+1,client_id=sqlc.arg(client_id)::uuid,target_root_id=sqlc.arg(target_root_id)::uuid,local_root=sqlc.arg(local_root)::text,snapshot=sqlc.arg(snapshot)::jsonb,updated_at=sqlc.arg(updated_at)::timestamptz WHERE id=sqlc.arg(id)::uuid AND version=sqlc.arg(expected_version)::bigint AND owner_id=sqlc.arg(owner_id)::uuid;

-- name: InsertUntrackedScopeReceipt :exec
INSERT INTO untracked_directory_scope_receipt(scope_id,owner_id,command_key,version,enabled,local_root,request_hash,input,snapshot)
VALUES(sqlc.arg(scope_id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(command_key)::text,sqlc.arg(version)::bigint,sqlc.arg(enabled)::boolean,sqlc.arg(local_root)::text,sqlc.arg(request_hash)::text,sqlc.arg(input)::jsonb,sqlc.arg(snapshot)::jsonb);

-- name: LockUntrackedScopeClient :one
SELECT id FROM download_client WHERE id=$1 FOR SHARE;

-- name: LockUntrackedScopeRoots :many
SELECT id FROM library_root WHERE id=sqlc.arg(target_root_id)::uuid OR path=sqlc.arg(source_path)::text OR path='/' OR starts_with(sqlc.arg(source_path)::text,path||'/') OR starts_with(path,sqlc.arg(source_path)::text||'/') ORDER BY id FOR UPDATE;

-- name: UntrackedScopeClock :one
SELECT clock_timestamp()::timestamptz AS now;

-- name: UntrackedScopeHistoryExists :one
SELECT (to_regclass('public.untracked_directory_scope_receipt') IS NOT NULL)::boolean AS exists;

-- name: UntrackedScopeRecognitionRoot :one
SELECT path FROM library_root WHERE id=$1;

-- name: UntrackedScopeRecognitionBlocked :one
SELECT EXISTS(SELECT 1 FROM untracked_directory_scope_receipt WHERE enabled AND (local_root=sqlc.arg(full_path)::text OR starts_with(sqlc.arg(full_path)::text,local_root||'/')))::boolean AS blocked;
