-- GetUntrackedOrganizationObservation: (uuid.UUID) ([]byte,error).
-- GetUntrackedOrganizationMetadataParams: Namespace,ExternalID string; returns ([]byte,error).
-- SQLREADY: source contracts below are frozen for Main's sqlc generation.
-- UUID=uuid.UUID, JSONB=[]byte, TEXT=string, BIGINT=int64,
-- TIMESTAMPTZ=pgtype.Timestamptz. All document queries return ([]byte,error).
-- GetUntrackedOrganizationByID/LockUntrackedOrganization: (uuid.UUID).
-- GetUntrackedOrganizationKeyParams: ScopeID,FileID uuid.UUID; CommandKey string.
-- GetUntrackedOrganizationConfirmation/GetUntrackedOrganizationUnits: (uuid.UUID).
-- GetUntrackedOrganizationPermit/GetUntrackedOrganizationCompletion: (uuid.UUID).
-- InsertUntrackedOrganizationParams: ID,OwnerID,ScopeID,FileID,ConfirmationID,
--   PlanID,StepID,JobID uuid.UUID; CommandKey,InputHash string; Material,Receipt []byte.
-- InsertUntrackedOrganizationUnitParams: OperationID,UnitID,MediaID uuid.UUID;
--   MetadataVersion,MediaMetadataVersion int64; UnitKey string; TmdbEpisodeID int64;
--   CandidateUnit,MediaIdentity []byte.
-- CreateUntrackedOrganizationJobParams: ID,OperationID uuid.UUID.
-- CreateUntrackedOrganizationPlanParams: ID,OperationID,RootID uuid.UUID;
--   Strategy,RootPath string; RootGeneration,RootDevice,RootInode int64;
--   CreatedAt pgtype.Timestamptz.
-- CreateUntrackedOrganizationStepParams: ID,PlanID,UnitID uuid.UUID;
--   SourcePath,TargetPath,Action,SourceRoot,SourceRel,TargetRel string;
--   SourceSize,SourceMtime,SourceDevice,SourceInode int64;
--   QualityProfile,TargetEvidence []byte; CreatedAt pgtype.Timestamptz.
-- InsertUntrackedOrganizationPermitParams: OperationID,JobID uuid.UUID;
--   JobOwner string; JobToken int64; ExecutionOwner string; ExecutionToken int64;
--   ContentSize int64; ContentSha256 []byte; ProofHash string;
--   IssuedAt pgtype.Timestamptz.
-- InsertUntrackedOrganizationObservationParams: ID,OperationID,JobID uuid.UUID;
--   JobOwner string; JobToken int64; Result []byte; RecordedAt pgtype.Timestamptz.
-- InsertUntrackedOrganizationCompletionParams: OperationID,StepID,FileID,
--   ObservationID uuid.UUID; Result []byte; RecordedAt pgtype.Timestamptz.
-- UntrackedOrganizationSealParams: OperationID,AuditID,EventID uuid.UUID.
-- All Insert/Create queries return error. No query grants filesystem authority.
-- Runtime callers MUST call postgres.GuardUntrackedOrganization in their explicit
-- pgx.Tx before writes and after SET CONSTRAINTS ALL IMMEDIATE. Full digest evidence
-- belongs to EXISTING transfer_content_evidence; these queries never mutate schema104.

-- name: GetUntrackedOrganizationByID :one
SELECT to_jsonb(o)::jsonb AS document FROM untracked_organization_operation o WHERE id=sqlc.arg(id)::uuid;

-- name: LockUntrackedOrganization :one
SELECT to_jsonb(o)::jsonb AS document FROM untracked_organization_operation o WHERE id=sqlc.arg(id)::uuid FOR UPDATE;

-- name: GetUntrackedOrganizationKey :one
SELECT to_jsonb(o)::jsonb AS document FROM untracked_organization_operation o
WHERE scope_id=sqlc.arg(scope_id)::uuid AND file_id=sqlc.arg(file_id)::uuid AND command_key=sqlc.arg(command_key)::text;

-- name: GetUntrackedOrganizationConfirmation :one
SELECT to_jsonb(c)::jsonb AS document FROM untracked_review_confirmation c WHERE id=sqlc.arg(id)::uuid;

-- name: GetUntrackedOrganizationUnits :one
SELECT coalesce(jsonb_agg(to_jsonb(u) ORDER BY u.unit_key,u.unit_id),'[]'::jsonb)::jsonb AS document
FROM untracked_organization_unit u WHERE operation_id=sqlc.arg(operation_id)::uuid;

-- name: GetUntrackedOrganizationPermit :one
SELECT to_jsonb(p)::jsonb AS document FROM untracked_organization_permit p WHERE operation_id=sqlc.arg(operation_id)::uuid;

-- name: GetUntrackedOrganizationCompletion :one
SELECT to_jsonb(c)::jsonb AS document FROM untracked_organization_completion c WHERE operation_id=sqlc.arg(operation_id)::uuid;

-- name: InsertUntrackedOrganization :exec
INSERT INTO untracked_organization_operation(id,owner_id,scope_id,file_id,confirmation_id,plan_id,step_id,job_id,command_key,input_hash,material,receipt)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(scope_id)::uuid,sqlc.arg(file_id)::uuid,sqlc.arg(confirmation_id)::uuid,
sqlc.arg(plan_id)::uuid,sqlc.arg(step_id)::uuid,sqlc.arg(job_id)::uuid,sqlc.arg(command_key)::text,sqlc.arg(input_hash)::text,sqlc.arg(material)::jsonb,sqlc.arg(receipt)::jsonb);

-- name: InsertUntrackedOrganizationUnit :exec
INSERT INTO untracked_organization_unit(operation_id,unit_id,media_id,metadata_version,media_metadata_version,unit_key,tmdb_episode_id,candidate_unit,media_identity)
VALUES(sqlc.arg(operation_id)::uuid,sqlc.arg(unit_id)::uuid,sqlc.arg(media_id)::uuid,sqlc.arg(metadata_version)::bigint,
sqlc.arg(media_metadata_version)::bigint,sqlc.arg(unit_key)::text,sqlc.arg(tmdb_episode_id)::bigint,sqlc.arg(candidate_unit)::jsonb,sqlc.arg(media_identity)::jsonb);

-- name: CreateUntrackedOrganizationJob :exec
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES(sqlc.arg(id)::uuid,'untracked_organization',(sqlc.arg(id)::uuid)::text,'queued',jsonb_build_object('operation_id',sqlc.arg(operation_id)::uuid),3,clock_timestamp(),clock_timestamp(),clock_timestamp());

-- name: CreateUntrackedOrganizationPlan :exec
INSERT INTO transfer_plan(id,operation_id,root_id,state,requires_approval,strategy,root_path,root_generation,root_device,root_inode,created_at,updated_at)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(operation_id)::uuid,sqlc.arg(root_id)::uuid,'approved',true,sqlc.arg(strategy)::text,sqlc.arg(root_path)::text,
sqlc.arg(root_generation)::bigint,sqlc.arg(root_device)::bigint,sqlc.arg(root_inode)::bigint,sqlc.arg(created_at)::timestamptz,sqlc.arg(created_at)::timestamptz);

-- name: CreateUntrackedOrganizationStep :exec
INSERT INTO transfer_step(id,plan_id,unit_id,source_path,target_path,action,state,step_order,source_size,source_mtime,source_root,source_rel,target_rel,source_device,source_inode,quality_profile,target_evidence,created_at,updated_at)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(plan_id)::uuid,sqlc.arg(unit_id)::uuid,sqlc.arg(source_path)::text,sqlc.arg(target_path)::text,sqlc.arg(action)::text,'pending',0,
sqlc.arg(source_size)::bigint,sqlc.arg(source_mtime)::bigint,sqlc.arg(source_root)::text,sqlc.arg(source_rel)::text,sqlc.arg(target_rel)::text,
sqlc.arg(source_device)::bigint,sqlc.arg(source_inode)::bigint,sqlc.arg(quality_profile)::jsonb,sqlc.arg(target_evidence)::jsonb,sqlc.arg(created_at)::timestamptz,sqlc.arg(created_at)::timestamptz);

-- name: InsertUntrackedOrganizationPermit :exec
INSERT INTO untracked_organization_permit(operation_id,job_id,job_owner,job_token,execution_owner,execution_token,content_size,content_sha256,proof_hash,issued_at)
VALUES(sqlc.arg(operation_id)::uuid,sqlc.arg(job_id)::uuid,sqlc.arg(job_owner)::text,sqlc.arg(job_token)::bigint,sqlc.arg(execution_owner)::text,
sqlc.arg(execution_token)::bigint,sqlc.arg(content_size)::bigint,sqlc.arg(content_sha256)::bytea,sqlc.arg(proof_hash)::text,sqlc.arg(issued_at)::timestamptz);

-- name: InsertUntrackedOrganizationObservation :exec
INSERT INTO untracked_organization_observation(id,operation_id,job_id,job_owner,job_token,result,recorded_at)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(operation_id)::uuid,sqlc.arg(job_id)::uuid,sqlc.arg(job_owner)::text,sqlc.arg(job_token)::bigint,sqlc.arg(result)::jsonb,sqlc.arg(recorded_at)::timestamptz);

-- name: InsertUntrackedOrganizationCompletion :exec
INSERT INTO untracked_organization_completion(operation_id,step_id,file_id,observation_id,result,recorded_at)
VALUES(sqlc.arg(operation_id)::uuid,sqlc.arg(step_id)::uuid,sqlc.arg(file_id)::uuid,sqlc.arg(observation_id)::uuid,sqlc.arg(result)::jsonb,sqlc.arg(recorded_at)::timestamptz);

-- Includes full rows to detect delayed constraints that alter non-semantic tails.
-- name: UntrackedOrganizationSeal :one
SELECT jsonb_build_array(
 (SELECT to_jsonb(o) FROM untracked_organization_operation o WHERE id=sqlc.arg(operation_id)::uuid),
 (SELECT coalesce(jsonb_agg(to_jsonb(u) ORDER BY u.unit_id),'[]'::jsonb) FROM untracked_organization_unit u WHERE operation_id=sqlc.arg(operation_id)::uuid),
 (SELECT to_jsonb(c) FROM untracked_review_confirmation c JOIN untracked_organization_operation o ON o.confirmation_id=c.id WHERE o.id=sqlc.arg(operation_id)::uuid),
 (SELECT to_jsonb(p) FROM transfer_plan p JOIN untracked_organization_operation o ON o.plan_id=p.id WHERE o.id=sqlc.arg(operation_id)::uuid),
 (SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]'::jsonb) FROM transfer_step s JOIN untracked_organization_operation o ON o.plan_id=s.plan_id WHERE o.id=sqlc.arg(operation_id)::uuid),
 (SELECT to_jsonb(j) FROM job j JOIN untracked_organization_operation o ON o.job_id=j.id WHERE o.id=sqlc.arg(operation_id)::uuid),
 (SELECT to_jsonb(a) FROM audit_log a WHERE id=sqlc.arg(audit_id)::uuid),
 (SELECT to_jsonb(e) FROM outbox_event e WHERE id=sqlc.arg(event_id)::uuid),
 (SELECT to_jsonb(p) FROM untracked_organization_permit p WHERE operation_id=sqlc.arg(operation_id)::uuid),
 (SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY v.id),'[]'::jsonb) FROM untracked_organization_observation v WHERE operation_id=sqlc.arg(operation_id)::uuid),
 (SELECT to_jsonb(c) FROM untracked_organization_completion c WHERE operation_id=sqlc.arg(operation_id)::uuid)
)::jsonb AS document;

-- Freeze canonical metadata present at preparation, including absence. Telemetry and
-- facts versions are intentionally excluded; identity trigger metadata versions remain.
-- name: GetUntrackedOrganizationMetadata :one
SELECT jsonb_build_array(
 (SELECT jsonb_build_array(m.id,m.title,m.original_title,m.year,m.media_type,m.metadata_version) FROM media m JOIN media_identity i ON i.media_id=m.id WHERE i.namespace=sqlc.arg(namespace)::text AND i.external_id=sqlc.arg(external_id)::text),
 (SELECT coalesce(jsonb_agg(jsonb_build_array(u.id,u.media_id,u.unit_key,u.season_number,u.episode_number,u.absolute_number,u.air_date,u.retired,u.metadata_version) ORDER BY u.id),'[]'::jsonb) FROM media_unit u JOIN media_identity i ON i.media_id=u.media_id WHERE i.namespace=sqlc.arg(namespace)::text AND i.external_id=sqlc.arg(external_id)::text),
 (SELECT coalesce(jsonb_agg(jsonb_build_array(e.unit_id,e.namespace,e.external_id,e.tmdb_series_id) ORDER BY e.unit_id,e.namespace),'[]'::jsonb) FROM media_unit_identity e JOIN media_unit u ON u.id=e.unit_id JOIN media_identity i ON i.media_id=u.media_id WHERE i.namespace=sqlc.arg(namespace)::text AND i.external_id=sqlc.arg(external_id)::text)
)::jsonb AS document;

-- name: GetUntrackedOrganizationObservation :one
SELECT to_jsonb(v)::jsonb AS document FROM untracked_organization_observation v WHERE id=sqlc.arg(id)::uuid;
