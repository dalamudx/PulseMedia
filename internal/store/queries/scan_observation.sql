
-- name: LockScanObservationRun :one
SELECT * FROM scan_run WHERE id=$1 FOR UPDATE;

-- name: LockScanObservationFile :one
SELECT * FROM library_file WHERE root_id=$1 AND rel_path=$2 AND retired_at IS NULL FOR UPDATE;

-- name: GetPreviousScanObservation :one
SELECT * FROM scan_observation WHERE scan_run_id=$1 AND rel_path=$2 ORDER BY id LIMIT 1;

-- name: ListScanFileSourceUnits :many
SELECT unit_id FROM library_file_unit WHERE file_id=$1
UNION
SELECT unit_id FROM unit_fact WHERE source_file_id=$1
ORDER BY unit_id;

-- name: RetireScanFileMappings :exec
UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id=$1 AND retired_at IS NULL;

-- name: RevokeScanFileSources :exec
UPDATE unit_fact SET revoked_at=clock_timestamp(), revocation_reason='scan_file_changed'
WHERE source_file_id=$1 AND revoked_at IS NULL AND stage IN ('imported','confirmed');

-- name: CreateObservedLibraryFile :one
INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint,last_mutation_seq)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;
