-- name: CreateMediaRequest :one
INSERT INTO media_request (
 id,owner_id,media_type,tmdb_id,seasons,include_specials,state,version,
 created_at,updated_at,creation_key,input_hash,creation_receipt
) VALUES (
 sqlc.arg(id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(media_type)::text,sqlc.arg(tmdb_id)::bigint,
 sqlc.arg(seasons)::integer[],sqlc.arg(include_specials)::boolean,'pending',1,
 sqlc.arg(created_at)::timestamptz,sqlc.arg(created_at)::timestamptz,
 sqlc.arg(creation_key)::text,sqlc.arg(input_hash)::text,sqlc.arg(creation_receipt)::jsonb
) RETURNING *;

-- name: GetMediaRequest :one
SELECT * FROM media_request WHERE id=sqlc.arg(id)::uuid;

-- name: LockMediaRequest :one
SELECT * FROM media_request WHERE id=sqlc.arg(id)::uuid FOR UPDATE;

-- name: GetMediaRequestByKey :one
SELECT * FROM media_request WHERE owner_id=sqlc.arg(owner_id)::uuid AND creation_key=sqlc.arg(creation_key)::text;

-- name: CountPendingMediaRequests :one
SELECT count(*)::bigint FROM media_request WHERE owner_id=sqlc.arg(owner_id)::uuid AND state='pending';

-- name: ListMediaRequests :many
SELECT * FROM media_request
WHERE (sqlc.arg(admin)::boolean OR owner_id=sqlc.arg(owner_id)::uuid)
 AND (sqlc.narg(before_id)::uuid IS NULL OR id<sqlc.narg(before_id)::uuid)
ORDER BY id DESC LIMIT sqlc.arg(page_limit)::integer;

-- name: CloseMediaRequest :one
UPDATE media_request SET
 state=sqlc.arg(state)::text,version=version+1,
 decision_actor_id=sqlc.arg(decision_actor_id)::uuid,
 decided_at=clock_timestamp(),updated_at=clock_timestamp(),reason=sqlc.arg(reason)::text,
 subscription_id=sqlc.narg(subscription_id)::uuid,
 decision_key=sqlc.arg(decision_key)::text,decision_hash=sqlc.arg(decision_hash)::text
WHERE id=sqlc.arg(request_id)::uuid AND state='pending' AND version=sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: ReadMediaRequestInbox :one
SELECT count(*)::bigint AS pending_count, statement_timestamp()::timestamptz AS observed_at
FROM media_request WHERE state='pending';

-- name: ListPendingMediaRequests :many
SELECT * FROM media_request
WHERE state='pending' AND (sqlc.narg(before_id)::uuid IS NULL OR id<sqlc.narg(before_id)::uuid)
ORDER BY id DESC LIMIT sqlc.arg(page_limit)::integer;

-- name: FlushMediaRequestConstraints :exec
SET CONSTRAINTS ALL IMMEDIATE;
