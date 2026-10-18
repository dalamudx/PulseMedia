-- Users
-- name: CreateUser :one
INSERT INTO app_user (
    id, username, password_hash, role, is_active, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetUserByID :one
SELECT * FROM app_user
WHERE id = $1;

-- name: LockUserForLogin :one
SELECT * FROM app_user
WHERE id = $1
FOR SHARE;

-- name: GetUserByUsername :one
SELECT * FROM app_user
WHERE LOWER(username) = LOWER($1);

-- name: CountUsers :one
SELECT COUNT(*) FROM app_user;

-- name: UpdateUserPassword :one
UPDATE app_user
SET password_hash = $2, updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: ListUsers :many
SELECT * FROM app_user
ORDER BY created_at ASC;

-- Sessions
-- name: CreateSession :one
INSERT INTO app_session (
    id, user_id, token_hash, csrf_token_hash, expires_at, created_at, last_seen_at
) VALUES (
    $1, $2, $3, $4, $5, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT s.*, u.username, u.role, u.is_active
FROM app_session s
JOIN app_user u ON s.user_id = u.id
WHERE s.token_hash = $1
  AND s.expires_at > statement_timestamp()
  AND u.is_active = true;

-- name: TouchSession :exec
UPDATE app_session
SET last_seen_at = statement_timestamp()
WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM app_session
WHERE id = $1;

-- name: DeleteExpiredSessions :exec
DELETE FROM app_session
WHERE expires_at <= statement_timestamp();

-- API Tokens
-- name: CreateAPIToken :one
INSERT INTO api_token (
    id, user_id, name, token_hash, scopes, expires_at, revoked_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, NULL, statement_timestamp()
) RETURNING *;

-- name: GetAPITokenByHash :one
SELECT t.*, u.username, u.role, u.is_active
FROM api_token t
JOIN app_user u ON t.user_id = u.id
WHERE t.token_hash = $1
  AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > statement_timestamp())
  AND u.is_active = true;

-- name: RevokeAPIToken :one
UPDATE api_token
SET revoked_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- Settings
-- name: GetSetting :one
SELECT * FROM setting
WHERE key = $1;

-- name: SetSetting :one
INSERT INTO setting (
    key, value, version, updated_at
) VALUES (
    $1, $2, 1, statement_timestamp()
) ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    version = setting.version + 1,
    updated_at = statement_timestamp()
RETURNING *;

-- name: GetSettingForUpdate :one
SELECT * FROM setting
WHERE key = $1
FOR UPDATE;

-- Audit
-- name: InsertAuditLog :one
INSERT INTO audit_log (
    id, actor_type, actor_id, action, target_type, target_id, payload, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, statement_timestamp()
) RETURNING *;

-- name: ListAuditLogs :many
SELECT * FROM audit_log
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- Idempotency
-- name: GetIdempotencyRecord :one
SELECT * FROM idempotency_record
WHERE actor_type = $1 AND actor_id = $2 AND idempotency_key = $3;

-- name: CreateIdempotencyRecord :one
INSERT INTO idempotency_record (
    id, actor_type, actor_id, idempotency_key, request_hash, response_code, response_body, resource_id, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, statement_timestamp()
) RETURNING *;


-- name: ListSettings :many
SELECT * FROM setting ORDER BY key;


-- name: LockTokenManagementUser :one
SELECT id, is_active, role FROM app_user WHERE id=$1 FOR SHARE;

-- name: ListOwnAPITokens :many
SELECT id, name, scopes, created_at, expires_at, revoked_at FROM api_token WHERE user_id=$1 ORDER BY created_at DESC,id;

-- name: GetOwnAPIToken :one
SELECT id, name, scopes, created_at, expires_at, revoked_at FROM api_token WHERE id=$1 AND user_id=$2;

-- name: RevokeOwnAPIToken :one
UPDATE api_token SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1 AND user_id=$2 RETURNING id, name, scopes, created_at, expires_at, revoked_at;

-- name: IsTokenExpiryFuture :one
SELECT sqlc.arg(expires_at)::timestamptz > clock_timestamp() AS valid;


-- name: LockSettingsUser :one
SELECT id, is_active, role FROM app_user WHERE id=$1 FOR SHARE;
