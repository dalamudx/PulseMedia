-- name: LockActiveAcquisitionLane :one
SELECT * FROM acquisition_reservation WHERE unit_id=$1 AND acquisition_lane='default' AND released_at IS NULL FOR UPDATE;
-- name: CreateAcquisitionLane :one
INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,source_root_id,source_generation,source_stamp)
VALUES($1,$2,$3,$4,$5,$6,$7)RETURNING *;
-- name: BindPhysicalAcquisitionTask :exec
UPDATE acquisition_reservation SET task_id=$2,updated_at=clock_timestamp() WHERE operation_id=$1 AND released_at IS NULL AND task_id IS NULL;
-- name: ReleaseFinishedAcquisitionLane :exec
UPDATE acquisition_reservation a SET released_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE a.operation_id=$1 AND a.released_at IS NULL
AND NOT EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=a.operation_id AND d.state='pending')
AND NOT EXISTS(SELECT 1 FROM unit_reservation r WHERE r.unit_id=a.unit_id AND r.state IN ('reserved','dispatched'));
-- name: GetAcquisitionSubmissionJob :one
SELECT * FROM job WHERE kind='download_submission' AND payload->>'operation_id'=$1::text ORDER BY created_at DESC,id DESC LIMIT 1;
-- name: ListPotentialReusableFiles :many
SELECT f.rel_path,r.health,r.baseline_completed,uf.quality_profile,uf.stage FROM library_file_unit fu
JOIN library_file f ON f.id=fu.file_id AND f.root_id<>$2 AND f.retired_at IS NULL
JOIN library_root r ON r.id=f.root_id
LEFT JOIN unit_fact uf ON uf.unit_id=fu.unit_id AND uf.source_file_id=f.id AND uf.root_id=f.root_id AND uf.stage='imported' AND uf.revoked_at IS NULL AND (uf.expires_at IS NULL OR uf.expires_at>clock_timestamp())
WHERE fu.unit_id=$1 AND fu.retired_at IS NULL ORDER BY f.root_id,f.id;
-- name: UpsertSharedRequirement :one
INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence)
VALUES($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT(operation_id,unit_id,root_id,actor_type,actor_id) DO UPDATE SET submission_fence=EXCLUDED.submission_fence,state='pending',released_at=NULL RETURNING *;
