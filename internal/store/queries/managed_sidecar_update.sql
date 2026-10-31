-- name: RecordManagedSidecarGeneration :exec
INSERT INTO managed_sidecar_content(id,file_id,unit_id,source_job_id,root_id,directory,name,identity,content,content_hash)
SELECT (x->>'id')::uuid,(x->>'file_id')::uuid,(x->>'unit_id')::uuid,(x->>'source_job_id')::uuid,(x->>'root_id')::uuid,x->>'directory',x->>'name',x->'identity',decode(x->>'content_hex','hex'),x->>'content_hash'
FROM jsonb_array_elements(sqlc.arg(records)::jsonb) x;

-- name: FindManagedSidecarSource :one
SELECT id,payload FROM job WHERE kind='media_sidecar' AND state='succeeded'
 AND payload->>'file_id'=sqlc.arg(file_id)::uuid::text AND payload->>'unit_id'=sqlc.arg(unit_id)::uuid::text
ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: GetManagedSidecarConfiguration :one
SELECT encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(s) ORDER BY key),'[]'::jsonb)::text,'UTF8')),'hex')::text FROM setting s;

-- name: ListManagedSidecarContents :many
SELECT DISTINCT ON(name) name,directory,identity,content,content_hash FROM managed_sidecar_content
WHERE file_id=sqlc.arg(file_id)::uuid AND unit_id=sqlc.arg(unit_id)::uuid
ORDER BY name,created_at DESC,id DESC;

-- name: InsertManagedSidecarPreview :exec
INSERT INTO managed_sidecar_update(id,owner_id,file_id,ticket,result,expires_at,state)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(file_id)::uuid,sqlc.arg(ticket)::jsonb,sqlc.arg(result)::jsonb,sqlc.arg(expires_at)::timestamptz,sqlc.arg(state)::text);

-- name: LockManagedSidecarUpdate :one
SELECT *,expires_at>clock_timestamp() AS valid_preview,COALESCE(lease_until>clock_timestamp(),false)::boolean AS valid_lease
FROM managed_sidecar_update WHERE id=sqlc.arg(id)::uuid FOR UPDATE;

-- name: ReadManagedSidecarCommand :one
SELECT id,result,state,file_id FROM managed_sidecar_update WHERE owner_id=sqlc.arg(owner_id)::uuid AND command_key=sqlc.arg(command_key)::text;

-- name: LockManagedSidecarCommand :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(scope)::text,0));

-- name: PermitManagedSidecarUpdate :execrows
UPDATE managed_sidecar_update SET permit=sqlc.arg(permit)::uuid,command_key=sqlc.arg(command_key)::text,
 lease_until=clock_timestamp()+interval '2 minutes',state='executing',result=sqlc.arg(result)::jsonb
WHERE id=sqlc.arg(id)::uuid AND permit IS NULL AND state='preview' AND expires_at>clock_timestamp();

-- name: FinishManagedSidecarUpdate :execrows
UPDATE managed_sidecar_update SET state=sqlc.arg(state)::text,result=sqlc.arg(result)::jsonb,released_at=CASE WHEN sqlc.arg(state)::text='completed' THEN clock_timestamp() ELSE NULL END
WHERE id=sqlc.arg(id)::uuid AND permit=sqlc.arg(permit)::uuid AND state='executing'
 AND lease_until>clock_timestamp() AND expires_at>clock_timestamp();

-- name: GuardManagedSidecarUpdate :one
SELECT id FROM managed_sidecar_update WHERE id=sqlc.arg(id)::uuid AND permit=sqlc.arg(permit)::uuid
 AND state='executing' AND lease_until>clock_timestamp() AND expires_at>clock_timestamp();

-- name: GetManagedSidecarClock :one
SELECT clock_timestamp()::timestamptz;

-- name: LockManagedSidecarConfiguration :exec
LOCK TABLE setting IN SHARE MODE;
