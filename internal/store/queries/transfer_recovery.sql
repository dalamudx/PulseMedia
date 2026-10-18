-- name: LockOriginalRecoveryPlan :one
SELECT * FROM transfer_plan WHERE id=$1 FOR UPDATE;
-- name: LockOriginalOrganizationJobs :many
SELECT * FROM job WHERE kind='download_organization' AND payload->>'task_id'=sqlc.arg(task_id)::text ORDER BY id FOR UPDATE;
-- name: ApproveOriginalRecoveryPlan :execrows
UPDATE transfer_plan SET state='approved',updated_at=clock_timestamp() WHERE id=$1 AND state IN ('review_required','failed','executing') AND (lease_until IS NULL OR lease_until<=clock_timestamp());
-- name: QueueOriginalRecoveryTask :execrows
UPDATE download_task SET organization_state='queued',organization_reason=NULL,updated_at=clock_timestamp() WHERE id=$1 AND state='completed' AND transfer_plan_id=$2 AND organization_state='review_required';
