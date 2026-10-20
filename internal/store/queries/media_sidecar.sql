-- All callers use transaction-bound queries. Enqueue belongs to the importing
-- PublishImported UoW, including when no notification/server recipient exists.
-- IDs are allocated by the caller's ports.IDGen. This is not Queue.Enqueue.
-- name: EnqueueMediaSidecarJob :one
INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before)
SELECT sqlc.arg(job_id)::uuid,'media_sidecar','media-sidecar:'||e.id::text,10,'queued',
 jsonb_build_object('version',1,'source_event_id',e.id,
                    'file_id',sqlc.arg(file_id)::uuid,'unit_id',sqlc.arg(unit_id)::uuid),
 1,clock_timestamp()
FROM outbox_event e
WHERE e.id=sqlc.arg(source_event_id)::uuid AND e.aggregate_type='media'
 AND e.aggregate_id=sqlc.arg(unit_id)::uuid::text AND e.event_type='media.imported'
 AND e.payload->>'file_id'=sqlc.arg(file_id)::uuid::text
 AND e.payload->>'unit_id'=sqlc.arg(unit_id)::uuid::text
RETURNING *;

-- name: SetMediaSidecarUTC :exec
SET LOCAL TIME ZONE 'UTC';

-- Lock first, then check the database clock in a fresh statement, before any
-- root/unit/file/media locks. A lease may expire while waiting for this lock.
-- name: LockMediaSidecarJob :one
SELECT * FROM job WHERE id=sqlc.arg(job_id)::uuid FOR UPDATE;

-- name: GuardMediaSidecarLease :one
SELECT id FROM job WHERE id=sqlc.arg(job_id)::uuid AND kind='media_sidecar'
 AND state='running' AND lease_owner=sqlc.arg(owner)::text
 AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp();

-- name: LockMediaSidecarRestoreState :one
SELECT isolated FROM restore_isolation WHERE singleton FOR SHARE;

-- name: LockMediaSidecarRoot :one
SELECT r.* FROM library_root r JOIN library_file f ON f.root_id=r.id
WHERE f.id=sqlc.arg(file_id)::uuid FOR NO KEY UPDATE OF r;

-- name: LockMediaSidecarUnit :one
SELECT * FROM media_unit WHERE id=sqlc.arg(unit_id)::uuid FOR NO KEY UPDATE;

-- name: LockMediaSidecarFile :one
SELECT * FROM library_file WHERE id=sqlc.arg(file_id)::uuid FOR NO KEY UPDATE;

-- name: LockMediaSidecarMedia :one
SELECT m.* FROM media m JOIN media_unit u ON u.media_id=m.id
WHERE u.id=sqlc.arg(unit_id)::uuid FOR NO KEY UPDATE OF m;

-- name: LockMediaSidecarMappings :many
SELECT * FROM library_file_unit WHERE file_id=sqlc.arg(file_id)::uuid
ORDER BY unit_id FOR SHARE;

-- name: LockMediaSidecarFacts :many
SELECT * FROM unit_fact WHERE source_file_id=sqlc.arg(file_id)::uuid
 AND unit_id=sqlc.arg(unit_id)::uuid ORDER BY id FOR SHARE;

-- name: LockMediaSidecarIdentities :many
SELECT * FROM media_identity WHERE media_id=sqlc.arg(media_id)::uuid ORDER BY id FOR SHARE;

-- Snapshot JSON is rendered and hashed in PostgreSQL with transaction-local UTC,
-- never compared by round-tripping TIMESTAMPTZ through Go JSON. No event path
-- participates in authority. Include complete active mappings and imported facts.
-- name: GetMediaSidecarSnapshot :one
WITH current_snapshot AS (
 SELECT jsonb_build_object(
  'payload',j.payload,'event',to_jsonb(e),
  'root',to_jsonb(r)-ARRAY['scan_token','mutation_seq','updated_at','name']::text[],
  'file',to_jsonb(f),'unit',to_jsonb(u),'media',to_jsonb(m),
  'root_identity',(SELECT jsonb_build_object('device',sf.root_device,'inode',sf.root_inode)
     FROM scan_finalization sf JOIN scan_run sr ON sr.id=sf.scan_run_id
     WHERE sf.root_id=r.id AND sr.generation=r.generation AND sr.state='succeeded'
       AND sf.completed_at IS NOT NULL ORDER BY sr.created_at,sr.id LIMIT 1),
  'identities',COALESCE((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id)
     FROM media_identity i WHERE i.media_id=m.id),'[]'::jsonb),
  'mappings',COALESCE((SELECT jsonb_agg(jsonb_build_object('mapping',to_jsonb(fu),'unit',to_jsonb(mu)) ORDER BY fu.unit_id)
     FROM library_file_unit fu JOIN media_unit mu ON mu.id=fu.unit_id
     WHERE fu.file_id=f.id AND fu.retired_at IS NULL),'[]'::jsonb),
  'facts',(SELECT jsonb_agg(to_jsonb(uf) ORDER BY uf.id) FROM unit_fact uf
     WHERE uf.source_file_id=f.id AND uf.unit_id=u.id AND uf.root_id=r.id
       AND uf.stage='imported' AND uf.revoked_at IS NULL),
  'poster_path',(SELECT a.poster_path FROM media_artwork a
     WHERE a.media_id=m.id AND a.provider='tmdb' AND a.namespace='tmdb.'||m.media_type
       AND a.metadata_version=m.metadata_version
       AND EXISTS(SELECT 1 FROM media_identity i WHERE i.media_id=m.id
          AND i.namespace=a.namespace AND i.external_id=a.external_id))
 ) AS snapshot
 FROM job j JOIN outbox_event e ON e.id=sqlc.arg(source_event_id)::uuid
 JOIN library_file f ON f.id=sqlc.arg(file_id)::uuid
 JOIN library_root r ON r.id=f.root_id
 JOIN media_unit u ON u.id=sqlc.arg(unit_id)::uuid
 JOIN media m ON m.id=u.media_id
 WHERE j.id=sqlc.arg(job_id)::uuid AND j.kind='media_sidecar'
  AND j.payload=jsonb_build_object('version',1,'source_event_id',e.id,'file_id',f.id,'unit_id',u.id)
  AND e.aggregate_type='media' AND e.aggregate_id=u.id::text AND e.event_type='media.imported'
  AND e.payload->>'file_id'=f.id::text AND e.payload->>'unit_id'=u.id::text
  AND f.retired_at IS NULL AND NOT u.retired AND r.health='online'
  AND r.baseline_completed AND r.media_type=m.media_type
  AND m.media_type IN ('movie','tv')
  AND EXISTS(SELECT 1 FROM library_file_unit fu WHERE fu.file_id=f.id AND fu.unit_id=u.id AND fu.retired_at IS NULL)
  AND EXISTS(SELECT 1 FROM unit_fact uf WHERE uf.source_file_id=f.id AND uf.unit_id=u.id
     AND uf.root_id=r.id AND uf.stage='imported' AND uf.revoked_at IS NULL)
)
SELECT snapshot,encode(sha256(convert_to(snapshot::text,'UTF8')),'hex')::text AS binding FROM current_snapshot;

-- name: CompleteMediaSidecarJob :one
UPDATE job SET state=CASE WHEN sqlc.arg(review_required)::boolean THEN 'dead' ELSE 'succeeded' END,
 result=sqlc.arg(result)::jsonb,
 error=CASE WHEN sqlc.arg(review_required)::boolean THEN 'review_required' ELSE NULL END,
 finished_at=clock_timestamp(),updated_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL
WHERE id=sqlc.arg(job_id)::uuid AND kind='media_sidecar' AND state='running'
 AND lease_owner=sqlc.arg(owner)::text AND lease_token=sqlc.arg(token)::bigint
 AND lease_until>clock_timestamp() AND payload=sqlc.arg(expected_payload)::jsonb
 AND EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND NOT isolated)
RETURNING *;

-- Expiry after possible physical publication is observation/manual review, never
-- automatic replay. Dead jobs retain their payload and any prior result.
-- name: RecoverExpiredMediaSidecarJobs :execrows
UPDATE job SET state='dead',error='review_required',lease_owner=NULL,lease_until=NULL,
 finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE kind='media_sidecar' AND state='running' AND lease_until<=clock_timestamp();
