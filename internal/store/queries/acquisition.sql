-- Acquisition & Reservation Queries

-- name: LockAcquisitionRoot :one
SELECT * FROM library_root WHERE id=$1 FOR NO KEY UPDATE;

-- name: LockAcquisitionUnit :one
SELECT * FROM media_unit WHERE id=$1 FOR NO KEY UPDATE;

-- name: CreateUnitReservation :one
INSERT INTO unit_reservation (
    id, unit_id, root_id, purpose, candidate_id, operation_id, state, expires_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, 'reserved', $7, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetActiveReservation :one
SELECT * FROM unit_reservation
WHERE unit_id = $1 AND root_id = $2 AND purpose = $3 AND state IN ('reserved', 'dispatched');

-- name: UpdateReservationState :one
UPDATE unit_reservation
SET state = $2, updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: CreateDownloadRequirement :one
INSERT INTO download_requirement (
    id, operation_id, unit_id, root_id, actor_type, actor_id, created_at,submission_fence
) VALUES (
    $1, $2, $3, $4, $5, $6, statement_timestamp(),sqlc.narg(submission_fence)
) RETURNING *;

-- name: ListAcquisitionOwnershipEvidence :many
SELECT f.rel_path, uf.stage, uf.quality_profile FROM library_file_unit fu
JOIN library_file f ON f.id=fu.file_id AND f.root_id=$2 AND f.retired_at IS NULL
LEFT JOIN unit_fact uf ON uf.unit_id=fu.unit_id AND uf.source_file_id=f.id AND uf.root_id=f.root_id
 AND uf.stage='imported' AND uf.revoked_at IS NULL AND (uf.expires_at IS NULL OR uf.expires_at>clock_timestamp())
WHERE fu.unit_id=$1 AND fu.retired_at IS NULL ORDER BY f.id,uf.created_at DESC;


-- name: ListRequirementsForOperation :many
SELECT * FROM download_requirement
WHERE operation_id = $1 ORDER BY root_id,unit_id,id;

-- name: DeleteRequirement :exec
UPDATE download_requirement SET state='released',released_at=clock_timestamp()
WHERE operation_id = $1 AND unit_id = $2 AND root_id = $3 AND actor_id = $4 AND actor_type=sqlc.arg(actor_type) AND state='pending';

-- name: CountRemainingRequirementsForOperation :one
SELECT count(*)::bigint FROM download_requirement
WHERE operation_id = $1 AND state='pending';
