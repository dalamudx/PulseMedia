-- Scan Queries

-- name: StartScanRun :one
WITH updated_root AS (
    UPDATE library_root
    SET scan_token = library_root.scan_token + 1, updated_at = statement_timestamp()
    WHERE library_root.id = $1
    RETURNING id, scan_token, generation, mutation_seq
)
INSERT INTO scan_run (
    id, root_id, scan_token, generation, start_mutation_seq, state, created_at
)
SELECT $2, r.id, r.scan_token, r.generation, r.mutation_seq, 'running', statement_timestamp()
FROM updated_root r
RETURNING *;

-- name: RecordObservation :one
INSERT INTO scan_observation (
    id, scan_run_id, root_id, rel_path, size_bytes, mtime_ns, fingerprint, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, statement_timestamp()
) RETURNING *;

-- name: GetActiveFilesForReconcile :many
SELECT id, rel_path, size_bytes, mtime_ns, last_mutation_seq
FROM library_file
WHERE root_id = $1 AND retired_at IS NULL AND last_mutation_seq <= $2;

-- name: FinishScanRunSuccess :one
WITH locked_root AS MATERIALIZED (
    SELECT r.id, r.health, r.generation, r.scan_token
    FROM library_root r
    WHERE r.id = (SELECT sr.root_id FROM scan_run sr WHERE sr.id = $1)
    FOR UPDATE OF r
), target_run AS (
    UPDATE scan_run s
    SET state = 'succeeded', files_observed = $2, files_added = $3,
        files_updated = $4, files_missing_candidates = $5,
        finished_at = statement_timestamp()
    FROM locked_root r
    WHERE s.id = $1 AND s.state = 'running'
      AND r.id = s.root_id AND r.health = 'online'
      AND r.generation = s.generation AND r.scan_token = s.scan_token
    RETURNING s.root_id
)
UPDATE library_root
SET baseline_completed = true, updated_at = statement_timestamp()
WHERE library_root.id = (SELECT root_id FROM target_run)
RETURNING *;

-- name: FinishScanRunFailed :one
UPDATE scan_run
SET state = $2, error = $3, finished_at = statement_timestamp()
WHERE scan_run.id = $1 AND scan_run.state IN ('running','finalizing') AND $2 IN ('partial', 'failed')
RETURNING *;

-- name: IncrementRootMutationSeq :one
UPDATE library_root
SET mutation_seq = library_root.mutation_seq + 1, updated_at = statement_timestamp()
WHERE library_root.id = $1
RETURNING mutation_seq;

-- name: UpdateRootHealth :one
UPDATE library_root
SET health = $2, updated_at = statement_timestamp()
WHERE library_root.id = $1
RETURNING *;

-- name: StartVerifiedScanRun :one
WITH updated_root AS (
    UPDATE library_root
    SET scan_token = library_root.scan_token + 1, updated_at = statement_timestamp()
    WHERE library_root.id = sqlc.arg(root_id)
      AND library_root.path = sqlc.arg(expected_path)
      AND library_root.media_type = sqlc.arg(expected_media_type)
      AND library_root.marker_filename = sqlc.arg(expected_marker_filename)
      AND library_root.generation = sqlc.arg(expected_generation)
      AND library_root.health_revision = sqlc.arg(expected_health_revision)
      AND library_root.health = 'online'
    RETURNING library_root.id, library_root.scan_token, library_root.generation, library_root.mutation_seq
)
INSERT INTO scan_run (id, root_id, scan_token, generation, start_mutation_seq, state, created_at)
SELECT sqlc.arg(run_id), r.id, r.scan_token, r.generation, r.mutation_seq, 'running', statement_timestamp()
FROM updated_root r
RETURNING *;

-- name: UpdateProbedRootHealth :one
UPDATE library_root
SET health = sqlc.arg(health), updated_at = statement_timestamp()
WHERE library_root.id = sqlc.arg(root_id)
  AND library_root.path = sqlc.arg(expected_path)
  AND library_root.media_type = sqlc.arg(expected_media_type)
  AND library_root.marker_filename = sqlc.arg(expected_marker_filename)
  AND library_root.generation = sqlc.arg(expected_generation)
  AND library_root.health_revision = sqlc.arg(expected_health_revision)
RETURNING *;
