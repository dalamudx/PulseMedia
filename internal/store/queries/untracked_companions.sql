-- name: ListUntrackedCompanions :many
SELECT jsonb_build_object('id',c.id,'operation_id',c.operation_id,'candidate',c.candidate,'action',c.action,
 'state',CASE WHEN c.state='running' AND c.lease_until<=clock_timestamp() THEN 'unknown' ELSE c.state END,'source_retained',true)::jsonb AS document
FROM untracked_companion c WHERE operation_id=$1 ORDER BY created_at,id;

-- name: ReadUntrackedCompanionSelection :one
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
SELECT selection_hash FROM untracked_companion c,input WHERE c.operation_id=(v->>'operation_id')::uuid AND c.command_key=v->>'command_key' LIMIT 1;

-- name: InsertUntrackedCompanion :exec
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
INSERT INTO untracked_companion(id,operation_id,command_key,selection_hash,candidate,action)
SELECT (v->>'id')::uuid,(v->>'operation_id')::uuid,v->>'command_key',v->>'selection_hash',v->'candidate',v->>'action' FROM input;

-- name: ClaimUntrackedCompanion :one
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
UPDATE untracked_companion c SET execution_owner=(v->>'owner')::uuid,execution_token=c.execution_token + 1,
 lease_until=clock_timestamp()+interval '2 minutes',state='running',updated_at=clock_timestamp()
FROM input WHERE c.operation_id=(v->>'operation_id')::uuid AND c.id=(v->>'id')::uuid AND c.state<>'completed'
 AND (c.lease_until IS NULL OR c.state='unknown' OR c.lease_until<=clock_timestamp())
RETURNING to_jsonb(c)::jsonb AS document;

-- name: GuardUntrackedCompanionExecution :one
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
SELECT to_jsonb(c)::jsonb AS document FROM untracked_companion c,input
WHERE c.operation_id=(v->>'operation_id')::uuid AND c.id=(v->>'id')::uuid AND c.execution_owner=(v->>'owner')::uuid
 AND c.execution_token=(v->>'token')::bigint AND c.state=v->>'state' AND c.lease_until>clock_timestamp() FOR UPDATE OF c;

-- name: SealUntrackedCompanion :execrows
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
UPDATE untracked_companion c SET content=v->'content',permit_owner=(v->>'owner')::uuid,permit_token=(v->>'token')::bigint,
 permit_issued_at=clock_timestamp(),updated_at=clock_timestamp()
FROM input WHERE c.operation_id=(v->>'operation_id')::uuid AND c.id=(v->>'id')::uuid AND c.execution_owner=(v->>'owner')::uuid
 AND c.execution_token=(v->>'token')::bigint AND c.state='running' AND c.lease_until>clock_timestamp() AND c.content IS NULL;

-- name: FinishUntrackedCompanion :execrows
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
UPDATE untracked_companion c SET state=v->>'state',result=NULLIF(v->'result','null'::jsonb),updated_at=clock_timestamp()
FROM input WHERE c.operation_id=(v->>'operation_id')::uuid AND c.id=(v->>'id')::uuid AND c.execution_owner=(v->>'owner')::uuid
 AND c.execution_token=(v->>'token')::bigint AND c.state='running' AND c.lease_until>clock_timestamp();
