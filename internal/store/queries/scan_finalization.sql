-- name: GetCurrentScanFinalization :one
SELECT f.*, s.generation, s.scan_token, s.start_mutation_seq
FROM scan_finalization f JOIN scan_run s ON s.id=f.scan_run_id
JOIN library_root r ON r.id=s.root_id
WHERE f.root_id=$1 AND s.state='finalizing' AND s.scan_token=r.scan_token;

-- name: LockScanFinalization :one
SELECT * FROM scan_finalization WHERE scan_run_id=$1 FOR UPDATE;

-- name: InsertScanFinalization :exec
INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,
 expected_marker_filename,expected_health_revision,root_device,root_inode,indexed_files)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,
 (SELECT count(*) FROM library_file file JOIN scan_run run ON run.id=$1
  WHERE file.root_id=run.root_id AND file.retired_at IS NULL
  AND file.last_mutation_seq<=run.start_mutation_seq AND file.created_at<=run.created_at));

-- name: SealScanRun :exec
UPDATE scan_run SET state='finalizing' WHERE id=$1 AND state='running';

-- name: ListScanReconciliationPage :many
SELECT f.id, NOT EXISTS(SELECT 1 FROM scan_observation o WHERE o.scan_run_id=sqlc.arg(run_id) AND o.root_id=f.root_id AND o.rel_path=f.rel_path) AS unseen
FROM library_file f JOIN scan_run s ON s.id=sqlc.arg(run_id)
WHERE f.root_id=s.root_id AND f.retired_at IS NULL
 AND f.last_mutation_seq<=s.start_mutation_seq AND f.created_at<=s.created_at
 AND (sqlc.narg(cursor_file_id)::uuid IS NULL OR f.id>sqlc.narg(cursor_file_id)::uuid)
ORDER BY f.id LIMIT 128;

-- name: AdvanceScanFinalization :exec
UPDATE scan_finalization SET cursor_file_id=$2,exhausted=$3 WHERE scan_run_id=$1;

-- name: AddScanMissingCount :exec
UPDATE scan_run SET files_missing_candidates=files_missing_candidates+$2 WHERE id=$1;

-- name: AddScanObservationCounts :exec
UPDATE scan_run SET files_observed=files_observed+1,files_added=files_added+$2,files_updated=files_updated+$3 WHERE id=$1 AND state='running';

-- name: CompleteFinalizedScan :exec
UPDATE scan_run SET state='succeeded',finished_at=clock_timestamp()
WHERE id=$1 AND state='finalizing';

-- name: CompleteScanFinalization :exec
UPDATE scan_finalization SET completed_at=clock_timestamp() WHERE scan_run_id=$1;

-- name: PublishScanBaseline :exec
UPDATE library_root SET baseline_completed=true,updated_at=clock_timestamp() WHERE id=$1;

-- name: GetTrustedScanRootIdentity :one
-- Bind a generation to its first successfully completed physical root. Failed or
-- interrupted walks cannot authorize a replacement mount.
SELECT f.root_device, f.root_inode
FROM scan_finalization f JOIN scan_run s ON s.id=f.scan_run_id
WHERE f.root_id=$1 AND s.generation=$2 AND s.state='succeeded'
  AND f.completed_at IS NOT NULL
ORDER BY s.created_at, s.id LIMIT 1;
