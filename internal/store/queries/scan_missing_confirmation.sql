-- name: GetMissingScanSeal :one
SELECT f.* FROM scan_finalization f JOIN scan_run s ON s.id=f.scan_run_id
JOIN library_root r ON r.id=f.root_id
WHERE f.root_id=$1 AND f.scan_run_id=$2 AND s.state='succeeded'
 AND f.completed_at IS NOT NULL AND NOT f.missing_review_required
 AND r.health='online' AND r.generation=s.generation AND r.scan_token=s.scan_token
 AND r.path=f.expected_path AND r.media_type=f.expected_media_type
 AND r.marker_filename=f.expected_marker_filename AND r.health_revision=f.completed_health_revision;

-- name: ListScanMissingToCheck :many
SELECT v.*,(clock_timestamp()+interval '5 seconds')::timestamptz AS valid_until
FROM qualified_scan_missing v
WHERE root_id=sqlc.arg(root_id) AND last_run_id=sqlc.arg(run_id)
 AND (sqlc.narg(after_file_id)::uuid IS NULL OR file_id>sqlc.narg(after_file_id)::uuid)
ORDER BY file_id LIMIT 128;

-- name: GetQualifiedScanMissing :one
SELECT * FROM qualified_scan_missing WHERE file_id=$1 AND first_run_id=$2 AND last_run_id=$3;

-- name: LockScanMissingCandidate :one
SELECT * FROM missing_candidate WHERE file_id=$1 AND first_run_id=$2 FOR UPDATE;

-- name: GetScanMissingReceipt :one
SELECT * FROM scan_missing_receipt WHERE file_id=$1 AND first_run_id=$2;

-- name: MissingCheckStillFresh :one
SELECT clock_timestamp() < sqlc.arg(valid_until)::timestamptz
 AND clock_timestamp() >= sqlc.arg(valid_until)::timestamptz-interval '5 seconds' AS fresh;

-- name: RevokeMissingFileSources :exec
UPDATE unit_fact SET revoked_at=clock_timestamp(),revocation_reason='scan_file_missing'
WHERE source_file_id=$1 AND revoked_at IS NULL AND stage IN('imported','confirmed');

-- name: CompleteMissingCandidate :exec
UPDATE missing_candidate SET state='cleared',cleared_at=clock_timestamp(),
 updated_at=clock_timestamp(),clear_reason=$3 WHERE file_id=$1 AND first_run_id=$2 AND state='open';

-- name: InsertScanMissingReceipt :exec
INSERT INTO scan_missing_receipt(file_id,first_run_id,last_run_id,root_id,decision,
 rel_path,last_mutation_seq,mapping_revision,file_fingerprint,size_bytes,mtime_ns)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11);
