-- Media Server Queries

-- name: CreateMediaServer :one
INSERT INTO media_server (
    id, name, server_type, url, api_key_encrypted, path_mapping, health, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, 'online', statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetMediaServerByID :one
SELECT * FROM media_server WHERE id = $1;

-- name: ListMediaServers :many
SELECT * FROM media_server
ORDER BY created_at ASC;

-- name: UpdateMediaServerHealth :one
UPDATE media_server
SET health = $2, updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: DeleteMediaServer :exec
DELETE FROM media_server WHERE id = $1;


-- name: UpdateMediaServer :one
UPDATE media_server
SET name = $2, server_type = $3, url = $4,
    api_key_encrypted = COALESCE($5, api_key_encrypted),
    updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;
