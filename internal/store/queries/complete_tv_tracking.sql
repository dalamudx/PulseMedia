-- name: ApplyCompleteTVTrackingObservation :one
UPDATE download_task
SET state = sqlc.arg(observed_state)::text,
    error = NULLIF(sqlc.arg(reason)::text, ''),
    progress = CASE WHEN sqlc.arg(observed_state)::text = 'completed' THEN 1 ELSE progress END,
    total_bytes = CASE WHEN sqlc.arg(observed_state)::text = 'completed' THEN sqlc.arg(total_bytes)::bigint ELSE total_bytes END,
    downloaded_bytes = CASE WHEN sqlc.arg(observed_state)::text = 'completed' THEN sqlc.arg(total_bytes)::bigint ELSE downloaded_bytes END,
    finished_at = CASE WHEN sqlc.arg(observed_state)::text = 'completed' THEN clock_timestamp() ELSE finished_at END,
    organization_state = CASE WHEN sqlc.arg(observed_state)::text = 'completed' THEN 'review_required' ELSE organization_state END,
    organization_reason = CASE WHEN sqlc.arg(observed_state)::text = 'completed' THEN 'dedicated full-set organization not implemented' ELSE organization_reason END,
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(task_id)::uuid AND state = 'tracking'
RETURNING id;
