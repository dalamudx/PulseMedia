-- name: GetOriginalRetrySubmission :one
SELECT * FROM job WHERE kind='download_submission' AND payload->>'operation_id'=sqlc.arg(operation_id)::text ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE;
-- name: RequeueOriginalUnsentSubmission :one
UPDATE job j SET state='queued', lease_token = (j.lease_token + 1), lease_owner=NULL, lease_until=NULL, not_before=clock_timestamp(), max_attempts=greatest(j.max_attempts,j.attempts+1), updated_at=clock_timestamp(), finished_at=NULL
WHERE j.id=sqlc.arg(id) AND j.kind='download_submission' AND j.state='dead' AND j.lease_token=sqlc.arg(expected_token) AND j.lease_owner IS NULL AND j.lease_until IS NULL
AND j.attempts >= j.max_attempts AND j.attempts < 2147483647 AND j.lease_token < 9223372036854775807
AND j.dedupe_key = j.payload->>'operation_id'
AND NOT EXISTS (SELECT 1 FROM download_task t WHERE t.operation_id::text = j.payload->>'operation_id')
RETURNING j.*;
