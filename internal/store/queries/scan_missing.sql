-- name: ClearSupersededMissingCandidate :exec
UPDATE missing_candidate c SET state='cleared',cleared_at=clock_timestamp(),
 updated_at=clock_timestamp(),clear_reason='scan source superseded'
FROM library_file file, scan_run first, scan_finalization origin,
 scan_run current, scan_finalization latest
WHERE c.file_id=sqlc.arg(file_id) AND c.state='open'
 AND file.id=c.file_id AND first.id=c.first_run_id AND origin.scan_run_id=first.id
 AND current.id=sqlc.arg(run_id) AND latest.scan_run_id=current.id
 AND c.first_run_id<>current.id
 AND (first.state<>'succeeded' OR first.generation<>current.generation
 OR origin.expected_path<>latest.expected_path
 OR origin.expected_media_type<>latest.expected_media_type
 OR origin.expected_marker_filename<>latest.expected_marker_filename
 OR origin.root_device<>latest.root_device OR origin.root_inode<>latest.root_inode
 OR origin.completed_health_revision IS DISTINCT FROM latest.expected_health_revision
 OR origin.missing_review_required OR file.last_mutation_seq>first.start_mutation_seq);

-- name: RecordScanMissingCandidate :exec
INSERT INTO missing_candidate(file_id,root_id,first_run_id,last_run_id)
SELECT file.id,file.root_id,s.id,s.id FROM library_file file
JOIN scan_run s ON s.root_id=file.root_id
JOIN scan_finalization f ON f.scan_run_id=s.id
WHERE file.id=sqlc.arg(file_id) AND s.id=sqlc.arg(run_id) AND s.state='finalizing'
 AND file.retired_at IS NULL AND file.last_mutation_seq<=s.start_mutation_seq
 AND file.created_at<=s.created_at
ON CONFLICT(file_id) WHERE state='open' DO UPDATE
SET last_run_id=EXCLUDED.last_run_id,updated_at=clock_timestamp()
WHERE missing_candidate.last_run_id<>EXCLUDED.last_run_id;

-- name: ClearObservedMissingCandidates :exec
UPDATE missing_candidate c SET state='cleared',cleared_at=clock_timestamp(),
 updated_at=clock_timestamp(),clear_reason='file observed'
FROM library_file file WHERE file.id=c.file_id AND file.root_id=sqlc.arg(root_id)
 AND file.rel_path=sqlc.arg(rel_path) AND c.state='open';

-- name: SetScanMissingReview :exec
UPDATE scan_finalization SET missing_review_required=$2 WHERE scan_run_id=$1;

-- name: RecordScanCompletedHealthRevision :exec
UPDATE scan_finalization f SET completed_health_revision=r.health_revision
FROM library_root r WHERE f.scan_run_id=$1 AND r.id=f.root_id;

-- name: ListQualifiedScanMissingCandidates :many
-- Read-only eligibility for a subsequent physical Stat; it authorizes no writes.
SELECT c.file_id,c.first_run_id,c.last_run_id,file.rel_path,
 file.last_mutation_seq,file.mapping_revision
FROM missing_candidate c JOIN library_file file ON file.id=c.file_id
JOIN scan_run first ON first.id=c.first_run_id
JOIN scan_run latest ON latest.id=c.last_run_id
JOIN scan_finalization origin ON origin.scan_run_id=first.id
JOIN scan_finalization final ON final.scan_run_id=latest.id
JOIN library_root root ON root.id=c.root_id
WHERE c.root_id=sqlc.arg(root_id) AND c.last_run_id=sqlc.arg(run_id)
 AND c.state='open' AND file.retired_at IS NULL
 AND first.state='succeeded' AND latest.state='succeeded'
 AND origin.completed_at IS NOT NULL AND final.completed_at IS NOT NULL
 AND first.id<>latest.id AND latest.created_at>=first.finished_at+interval '5 minutes'
 AND NOT origin.missing_review_required AND NOT final.missing_review_required
 AND root.health='online' AND root.generation=latest.generation
 AND root.scan_token=latest.scan_token AND root.health_revision=final.completed_health_revision
 AND root.path=final.expected_path AND root.media_type=final.expected_media_type
 AND root.marker_filename=final.expected_marker_filename
 AND first.generation=latest.generation AND origin.expected_path=final.expected_path
 AND origin.expected_media_type=final.expected_media_type
 AND origin.expected_marker_filename=final.expected_marker_filename
 AND origin.root_device=final.root_device AND origin.root_inode=final.root_inode
 AND origin.completed_health_revision=final.expected_health_revision
 AND file.last_mutation_seq<=first.start_mutation_seq
 AND file.last_mutation_seq<=latest.start_mutation_seq
ORDER BY c.file_id LIMIT 128;
