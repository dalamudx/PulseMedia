-- Acquire in a separate statement before enqueue, so a lock waiter uses a fresh
-- READ COMMITTED snapshot for the historical-job check below.
-- name: LockMediaSubtitleEnqueue :one
SELECT id FROM library_file WHERE id=sqlc.arg(file_id)::uuid FOR NO KEY UPDATE;
-- name: EnqueueMediaSubtitleJob :exec
INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before)
SELECT sqlc.arg(job_id)::uuid,'media_subtitle',
 'media-subtitle:'||f.id::text||':'||f.mapping_revision::text,10,'queued',
 jsonb_build_object('version',1,'source_event_id',e.id,'file_id',f.id,'unit_id',sqlc.arg(unit_id)::uuid),
 1,clock_timestamp()
FROM outbox_event e JOIN library_file f ON f.id=sqlc.arg(file_id)::uuid
WHERE e.id=sqlc.arg(source_event_id)::uuid AND e.aggregate_type='media'
 AND e.aggregate_id=sqlc.arg(unit_id)::uuid::text AND e.event_type='media.imported'
 AND e.payload->>'file_id'=f.id::text AND e.payload->>'unit_id'=sqlc.arg(unit_id)::uuid::text
 AND NOT EXISTS (SELECT 1 FROM job j WHERE j.kind='media_subtitle'
   AND j.dedupe_key='media-subtitle:'||f.id::text||':'||f.mapping_revision::text)
ON CONFLICT(kind,dedupe_key) WHERE dedupe_key IS NOT NULL AND state IN ('queued','running') DO NOTHING;

-- Expiry may follow an unconfirmed physical publication; never replay it.
-- name: RecoverExpiredMediaSubtitleJobs :execrows
UPDATE job SET state='dead',error='review_required',lease_owner=NULL,lease_until=NULL,
 finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE kind='media_subtitle' AND state='running' AND lease_until<=clock_timestamp();
