-- name: ExternalTransferTargetSchemaReady :one
SELECT (to_regclass('public.untracked_organization_operation') IS NOT NULL)::boolean AS ready;

-- name: ExternalTransferTargetBlocked :one
SELECT EXISTS (
 SELECT 1 FROM untracked_organization_operation o
 JOIN untracked_review_confirmation c ON c.id=o.confirmation_id
 WHERE (c.receipt->'material'->'scope'->>'target_root_id')::uuid=sqlc.arg(root_id)::uuid
 AND c.receipt->'material'->'input'->>'target_relative_path'=sqlc.arg(relative_path)::text
) AS blocked;

-- name: ExternalTransferOperationForJob :one
SELECT id FROM untracked_organization_operation WHERE job_id=$1;

-- name: ExternalTransferPermitRow :one
SELECT to_jsonb(p)::jsonb AS document FROM untracked_organization_permit p WHERE operation_id=$1;

-- name: ExternalTransferClaimUnpublished :one
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
UPDATE transfer_plan p SET state='executing',execution_owner=v->>'execution_owner',execution_token=p.execution_token+1,
 lease_until=clock_timestamp()+interval '2 minutes',updated_at=clock_timestamp()
FROM input,untracked_organization_operation o,job j
WHERE p.id=o.plan_id AND o.id=(v->>'operation_id')::uuid AND j.id=o.job_id AND j.kind='untracked_organization'
 AND j.state='running' AND j.lease_owner=v->>'job_owner' AND j.lease_token=(v->>'job_token')::bigint AND j.lease_until>clock_timestamp()
 AND p.state IN('approved','executing','review_required') AND NOT EXISTS(SELECT 1 FROM untracked_organization_permit x WHERE x.operation_id=o.id)
RETURNING p.*;

-- name: ExternalTransferSealContent :execrows
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
INSERT INTO transfer_content_evidence(step_id,content)
SELECT o.step_id,v->'content' FROM input,untracked_organization_operation o,transfer_plan p,transfer_step s
WHERE o.id=(v->>'operation_id')::uuid AND p.id=o.plan_id AND s.id=o.step_id AND s.plan_id=p.id
 AND p.state='executing' AND s.state='running' AND s.target_evidence->>'untracked_organization_id'=o.id::text
 AND NOT EXISTS(SELECT 1 FROM untracked_organization_permit x WHERE x.operation_id=o.id);

-- name: ExternalTransferInsertPermit :execrows
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
INSERT INTO untracked_organization_permit(operation_id,job_id,job_owner,job_token,execution_owner,execution_token,content_size,content_sha256,proof_hash,issued_at)
SELECT (v->>'operation_id')::uuid,(v->>'job_id')::uuid,v->>'job_owner',(v->>'job_token')::bigint,
 v->>'execution_owner',(v->>'execution_token')::bigint,(v->>'content_size')::bigint,(v->>'content_sha256')::bytea,v->>'proof_hash',(v->>'issued_at')::timestamptz FROM input;

-- name: ExternalTransferInsertObservation :one
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
INSERT INTO untracked_organization_observation(id,operation_id,job_id,job_owner,job_token,result,recorded_at)
SELECT (v->>'id')::uuid,(v->>'operation_id')::uuid,(v->>'job_id')::uuid,v->>'job_owner',(v->>'job_token')::bigint,v->'result',(v->>'recorded_at')::timestamptz FROM input
RETURNING id;

-- name: ExternalTransferInsertCompletion :execrows
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
INSERT INTO untracked_organization_completion(operation_id,step_id,file_id,observation_id,result,recorded_at)
SELECT (v->>'operation_id')::uuid,(v->>'step_id')::uuid,(v->>'file_id')::uuid,(v->>'observation_id')::uuid,v->'result',(v->>'recorded_at')::timestamptz FROM input;

-- name: ExternalTransferFinishPlan :execrows
UPDATE transfer_plan p SET state='completed',finished_at=clock_timestamp(),updated_at=clock_timestamp()
FROM untracked_organization_operation o,untracked_organization_completion c
WHERE p.id=o.plan_id AND c.operation_id=o.id AND o.id=$1 AND p.state IN('executing','review_required')
 AND NOT EXISTS(SELECT 1 FROM transfer_step s WHERE s.plan_id=p.id AND s.state<>'completed');

-- name: ExternalTransferFinishJob :execrows
WITH input AS(SELECT sqlc.arg(document)::jsonb v)
UPDATE job j SET state='succeeded',finished_at=clock_timestamp(),updated_at=clock_timestamp(),error=NULL,result=v->'result'
FROM input,untracked_organization_operation o,untracked_organization_completion c
WHERE o.id=(v->>'operation_id')::uuid AND j.id=o.job_id AND c.operation_id=o.id AND j.state='running'
 AND j.lease_owner=v->>'job_owner' AND j.lease_token=(v->>'job_token')::bigint AND j.lease_until>clock_timestamp();

-- name: ExternalTransferRecoverJobs :many
WITH candidates AS MATERIALIZED(
 SELECT j.id FROM job j JOIN untracked_organization_operation o ON o.job_id=j.id
 WHERE j.kind='untracked_organization' AND j.state='running' AND j.lease_until<=clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM untracked_organization_completion c WHERE c.operation_id=o.id)
 ORDER BY j.lease_until,j.id LIMIT sqlc.arg(page_size) FOR UPDATE OF j SKIP LOCKED
)
UPDATE job j SET state=CASE WHEN j.attempts<j.max_attempts THEN 'queued' ELSE 'dead' END,
 lease_owner=NULL,lease_until=NULL,not_before=clock_timestamp(),updated_at=clock_timestamp(),error='untracked organization interrupted'
FROM candidates c WHERE j.id=c.id AND j.state='running' AND j.lease_until<=clock_timestamp()
RETURNING j.id;
