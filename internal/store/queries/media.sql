-- Media
-- name: CreateMedia :one
INSERT INTO media (
    id, title, original_title, year, media_type, metadata_version, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, 1, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetMediaByID :one
SELECT * FROM media WHERE id = $1;

-- Identity
-- name: CreateMediaIdentity :one
INSERT INTO media_identity (
    id, media_id, namespace, external_id, created_at
) VALUES (
    $1, $2, $3, $4, statement_timestamp()
) RETURNING *;

-- name: GetMediaByIdentity :one
SELECT m.*, i.namespace, i.external_id
FROM media_identity i
JOIN media m ON i.media_id = m.id
WHERE i.namespace = $1 AND i.external_id = $2;


-- name: GetMediaTMDBIdentity :one
SELECT external_id, namespace FROM media_identity
WHERE media_id = $1 AND namespace LIKE 'tmdb.%'
LIMIT 1;

-- Units
-- name: CreateMediaUnit :one
INSERT INTO media_unit (
    id, media_id, unit_key, air_date, retired, facts_version, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, false, 1, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetMediaUnit :one
SELECT * FROM media_unit
WHERE media_id = $1 AND unit_key = $2;

-- name: ListMediaUnits :many
SELECT * FROM media_unit
WHERE media_id = $1 AND retired = false
ORDER BY unit_key ASC;

-- Library Root
-- name: CreateLibraryRoot :one
INSERT INTO library_root (
    id, path, media_type, name, health, generation, scan_token, mutation_seq, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, 'online', 1, 0, 0, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetFirstOnlineRootForType :one
SELECT * FROM library_root
WHERE media_type = $1 AND health = 'online'
ORDER BY created_at ASC
LIMIT 1;

-- name: GetLibraryRootByID :one
SELECT * FROM library_root WHERE id = $1;

-- name: GetLibraryRootByPath :one
SELECT * FROM library_root WHERE path = $1;

-- Library File
-- name: CreateLibraryFile :one
INSERT INTO library_file (
    id, root_id, rel_path, size_bytes, mtime_ns, file_fingerprint, last_mutation_seq, retired_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, NULL, statement_timestamp()
) RETURNING *;

-- name: GetActiveFileByPath :one
SELECT * FROM library_file
WHERE root_id = $1 AND rel_path = $2 AND retired_at IS NULL;

-- name: RetireFileByPath :exec
UPDATE library_file
SET retired_at = statement_timestamp()
WHERE root_id = $1 AND rel_path = $2 AND retired_at IS NULL;

-- Multi-Unit Mapping
-- name: LinkFileUnit :exec
INSERT INTO library_file_unit (
    file_id, unit_id, created_at
) VALUES (
    $1, $2, statement_timestamp()
) ON CONFLICT (file_id, unit_id) DO UPDATE SET retired_at=NULL WHERE library_file_unit.retired_at IS NOT NULL;

-- name: ListUnitsForFile :many
SELECT u.* FROM library_file_unit fu
JOIN media_unit u ON fu.unit_id = u.id
WHERE fu.file_id = $1 AND fu.retired_at IS NULL;

-- name: ListFilesForUnit :many
SELECT f.* FROM library_file_unit fu
JOIN library_file f ON fu.file_id = f.id
WHERE fu.unit_id = $1 AND fu.retired_at IS NULL AND f.retired_at IS NULL;

-- Facts
-- name: CreateUnitFact :one
INSERT INTO unit_fact (
    id, unit_id, stage, source_file_id, source_task_id, root_id, quality_profile, expires_at, revoked_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, NULL, statement_timestamp()
) RETURNING *;

-- name: ListActiveFactsForUnit :many
SELECT * FROM unit_fact
WHERE unit_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: GetFileByID :one
SELECT * FROM library_file WHERE id = $1;


-- name: CreateTVUnit :one
INSERT INTO media_unit (
    id, media_id, unit_key, season_number, episode_number, absolute_number, air_date, retired, facts_version, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, false, 1, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: ListMediaUnitsBySeason :many
SELECT * FROM media_unit
WHERE media_id = $1 AND season_number = $2 AND retired = false
ORDER BY episode_number ASC;


-- name: RevokeUnitFactsForFile :exec
UPDATE unit_fact
SET revoked_at = statement_timestamp()
WHERE source_file_id = $1 AND revoked_at IS NULL;

-- name: RetireFileByID :exec
UPDATE library_file
SET retired_at = statement_timestamp()
WHERE id = $1 AND retired_at IS NULL;
