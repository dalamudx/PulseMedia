-- name: LockCompleteTVOrganizationTracking :one
SELECT * FROM job WHERE id=$1 FOR SHARE;

-- name: GetCompleteTVTransferEvidence :one
SELECT * FROM complete_tv_transfer_evidence WHERE operation_id=$1;

-- name: CreateCompleteTVTransferEvidence :exec
INSERT INTO complete_tv_transfer_evidence(plan_id,task_id,operation_id,tracking_job_id,tracking_result,baseline)
VALUES($1,$2,$3,$4,$5,$6);

-- name: ReadCompleteTVTransferState :one
SELECT pulse_complete_tv_transfer_state(sqlc.arg(operation_id)::uuid,sqlc.arg(plan_id)::uuid)::jsonb AS state;

-- name: ValidateCompleteTVTransfer :one
SELECT pulse_complete_tv_transfer_valid($1::uuid)::boolean AS valid;

-- name: QueueCompleteTVOrganization :one
UPDATE download_task SET organization_state='queued',organization_reason=NULL,transfer_plan_id=$2,updated_at=clock_timestamp()
WHERE id=$1 AND state='completed' AND organization_state='review_required' AND transfer_plan_id IS NULL RETURNING *;

-- Freeze all lane columns except the explicit terminal release. The shared
-- release also rewrites updated_at, which is immutable organization evidence.
-- name: ReleaseCompleteTVOrganizationLanes :exec
UPDATE acquisition_reservation a SET released_at=clock_timestamp()
WHERE a.operation_id=$1 AND a.released_at IS NULL
AND NOT EXISTS(SELECT 1 FROM download_requirement d WHERE d.operation_id=a.operation_id AND d.state='pending')
AND NOT EXISTS(SELECT 1 FROM unit_reservation r WHERE r.unit_id=a.unit_id AND r.state IN ('reserved','dispatched'));
