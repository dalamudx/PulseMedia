-- name: LockDownloadAcquisition :exec
SELECT pg_advisory_xact_lock(hashtextextended('acquisition:'||$1::uuid::text,0));
-- name: GetSubmissionTask :one
SELECT * FROM download_task WHERE id=$1;
-- name: LockDownloadTaskUnits :many
SELECT u.* FROM media_unit u WHERE EXISTS(SELECT 1 FROM unit_reservation r JOIN download_task t ON t.operation_id=r.operation_id WHERE r.unit_id=u.id AND t.id=$1) ORDER BY u.id FOR NO KEY UPDATE;
-- name: MarkSubmissionOutcomeUnknown :one
UPDATE download_task SET state='unknown',error='downloader submission outcome unknown',updated_at=clock_timestamp() WHERE id=$1 AND state='submitting' RETURNING *;
-- name: LockSubmissionMedia :one
SELECT * FROM media WHERE id=$1 FOR NO KEY UPDATE;
-- name: GetDownloadRequirement :one
SELECT * FROM download_requirement WHERE id=$1;
-- name: LockSubmissionIndexer :one
SELECT * FROM indexer WHERE id=$1 FOR SHARE;
-- name: LockSubmissionCandidate :one
SELECT * FROM release_candidate WHERE id=$1 FOR SHARE;
-- name: GetAuthorizedSubmissionJob :one
SELECT * FROM job WHERE id=$1 AND kind IN ('download_submission','episode_download_submission') AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp();
-- name: LockSubmissionJobsByOperation :many
SELECT * FROM job WHERE kind IN ('download_submission','episode_download_submission') AND payload->>'operation_id'=$1::text AND state IN ('queued','running') ORDER BY id FOR UPDATE;
-- name: CancelUnsentSubmissionJobs :exec
UPDATE job SET state='dead',error='all requirements released before submission',lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE kind IN ('download_submission','episode_download_submission') AND payload->>'operation_id'=$1::text AND state IN ('queued','running');
-- name: ReleaseUnsentOperationReservations :exec
UPDATE unit_reservation SET state='released',updated_at=clock_timestamp() WHERE operation_id=$1 AND state IN ('reserved','dispatched');
