-- Indexer Queries

-- name: CreateIndexer :one
INSERT INTO indexer (
    id, name, url, api_key_encrypted, categories, config_version, health, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, 1, 'online', statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetIndexerByID :one
SELECT * FROM indexer WHERE id = $1;

-- name: ListIndexers :many
SELECT * FROM indexer
ORDER BY created_at ASC;

-- name: UpdateIndexerHealth :one
UPDATE indexer
SET health = $2, rate_limit_until = $3, updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;


-- name: UpdateIndexer :one
UPDATE indexer
SET name = $2, url = $3,
    api_key_encrypted = COALESCE($4, api_key_encrypted),
    categories = COALESCE($5, categories), config_version = config_version + 1,
    updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- Queries
-- name: CreateRunningQuery :one
INSERT INTO indexer_query (
    id, indexer_id, query_type, keyword, page, config_version, state, results_count, created_at, rss_job_id
) VALUES (
    $1, $2, $3, $4, $5, $6, 'running', 0, statement_timestamp(), sqlc.narg(rss_job_id)
) RETURNING *;

-- name: GetActiveQuery :one
SELECT * FROM indexer_query
WHERE indexer_id = $1 AND keyword = $2 AND page = $3 AND config_version = $4 AND query_type = $5 AND state = 'running' AND created_at > clock_timestamp()-interval '60 seconds';

-- name: FinishQuerySuccess :one
UPDATE indexer_query
SET state = 'succeeded', results_count = $2, finished_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: FinishQueryFailed :one
UPDATE indexer_query
SET state = 'failed', error = $2, finished_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- Release Candidates
-- name: UpsertReleaseCandidate :one
INSERT INTO release_candidate (
    id, indexer_id, title, guid, link_encrypted, info_hash, size_bytes,
    seeders, leechers, publish_date, quality_profile, matched_media_id, matched_unit_id, created_at,source_config_version
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, statement_timestamp(),sqlc.arg(source_config_version)
) ON CONFLICT (indexer_id, guid,source_config_version) DO UPDATE
SET seeders = EXCLUDED.seeders,
    leechers = EXCLUDED.leechers,
    quality_profile = release_candidate.quality_profile,
    matched_media_id = COALESCE(EXCLUDED.matched_media_id, release_candidate.matched_media_id),
    matched_unit_id = COALESCE(EXCLUDED.matched_unit_id, release_candidate.matched_unit_id)
RETURNING *;

-- name: GetCandidateByID :one
SELECT * FROM release_candidate WHERE id = $1;

-- name: ListCandidatesByUnit :many
SELECT * FROM release_candidate
WHERE matched_unit_id = $1 AND EXISTS(SELECT 1 FROM indexer i WHERE i.id=release_candidate.indexer_id AND i.config_version=release_candidate.source_config_version)
ORDER BY seeders DESC, created_at DESC;

-- name: DeleteIndexer :exec
DELETE FROM indexer WHERE id = $1;
