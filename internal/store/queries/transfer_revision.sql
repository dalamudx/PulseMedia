-- name: ReplaceReviewedDownloadPlan :execrows
UPDATE download_task SET transfer_plan_id=sqlc.arg(new_plan_id),organization_state='queued',organization_reason=NULL,updated_at=clock_timestamp()
WHERE id=sqlc.arg(task_id) AND transfer_plan_id=sqlc.arg(previous_plan_id) AND state='completed' AND organization_state='review_required';
