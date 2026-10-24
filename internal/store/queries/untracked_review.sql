-- JSON document results are []byte; UUID arguments use uuid.UUID, JSONB []byte,
-- TEXT string, BIGINT int64, INTEGER int32, TIMESTAMPTZ pgtype.Timestamptz.
-- New query generation belongs to Main after this source is frozen.
-- SQLREADY expected Go contracts (context.Context is always first):
-- GetUntrackedReviewFile / LockUntrackedReviewFile: (uuid.UUID) ([]byte,error).
-- GetUntrackedReviewRequest / LockUntrackedReviewRequest: (uuid.UUID) ([]byte,error).
-- GetUntrackedReviewRequestKeyParams: ScopeID,FileID uuid.UUID; CommandKey string.
-- GetUntrackedReviewRequestReceipt: (uuid.UUID) ([]byte,error).
-- InsertUntrackedReviewRequestParams: ID,OwnerID,ScopeID,FileID,JobID uuid.UUID;
--   CommandKey,InputHash string; Request,ScopeSnapshot,FileSnapshot []byte.
-- InsertUntrackedReviewRequestReceiptParams: RequestID uuid.UUID; Receipt []byte.
-- CreateUntrackedReviewJobParams: ID,RequestID uuid.UUID.
-- GetUntrackedReviewResult: (uuid.UUID) ([]byte,error).
-- InsertUntrackedReviewResultParams: RequestID uuid.UUID; MaterialHash string;
--   Review []byte; RecordedAt pgtype.Timestamptz.
-- FinishUntrackedReviewJobParams: Result []byte; ID uuid.UUID; Owner string; Token int64.
-- GetUntrackedConfirmationKeyParams: ScopeID,FileID uuid.UUID; CommandKey string.
-- InsertUntrackedConfirmationParams: ID,OwnerID,ScopeID,FileID,ReviewID uuid.UUID;
--   CommandKey,InputHash string; Receipt []byte.
-- UntrackedReviewSealParams: RequestID,AuditID,EventID uuid.UUID.
-- RecoverUntrackedReviewJobs: (int32) ([]uuid.UUID,error).
-- All *Key and Seal queries return ([]byte,error); Insert/Create return error;
-- Finish returns (int64,error).

-- name: GetUntrackedReviewFile :one
SELECT to_jsonb(f)::jsonb AS document FROM untracked_discovered_file f WHERE id=sqlc.arg(id)::uuid;

-- name: LockUntrackedReviewFile :one
SELECT to_jsonb(f)::jsonb AS document FROM untracked_discovered_file f WHERE id=sqlc.arg(id)::uuid FOR UPDATE;

-- name: GetUntrackedReviewRequest :one
SELECT to_jsonb(r)::jsonb AS document FROM untracked_review_request r WHERE id=sqlc.arg(id)::uuid;

-- name: LockUntrackedReviewRequest :one
SELECT to_jsonb(r)::jsonb AS document FROM untracked_review_request r WHERE id=sqlc.arg(id)::uuid FOR UPDATE;

-- name: GetUntrackedReviewRequestKey :one
SELECT to_jsonb(r)::jsonb AS document FROM untracked_review_request r
WHERE scope_id=sqlc.arg(scope_id)::uuid AND file_id=sqlc.arg(file_id)::uuid AND command_key=sqlc.arg(command_key)::text;

-- name: GetUntrackedReviewRequestReceipt :one
SELECT receipt FROM untracked_review_request_receipt WHERE request_id=sqlc.arg(request_id)::uuid;

-- name: InsertUntrackedReviewRequest :exec
INSERT INTO untracked_review_request(id,owner_id,scope_id,file_id,job_id,command_key,input_hash,request,scope_snapshot,file_snapshot)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(scope_id)::uuid,sqlc.arg(file_id)::uuid,sqlc.arg(job_id)::uuid,
sqlc.arg(command_key)::text,sqlc.arg(input_hash)::text,sqlc.arg(request)::jsonb,sqlc.arg(scope_snapshot)::jsonb,sqlc.arg(file_snapshot)::jsonb);

-- name: InsertUntrackedReviewRequestReceipt :exec
INSERT INTO untracked_review_request_receipt(request_id,receipt) VALUES(sqlc.arg(request_id)::uuid,sqlc.arg(receipt)::jsonb);

-- name: CreateUntrackedReviewJob :exec
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,not_before,created_at,updated_at)
VALUES(sqlc.arg(id)::uuid,'untracked_review',(sqlc.arg(id)::uuid)::text,'queued',jsonb_build_object('request_id',sqlc.arg(request_id)::uuid),3,clock_timestamp(),clock_timestamp(),clock_timestamp());

-- name: GetUntrackedReviewResult :one
SELECT to_jsonb(r)::jsonb AS document FROM untracked_review_result r WHERE request_id=sqlc.arg(request_id)::uuid;

-- name: InsertUntrackedReviewResult :exec
INSERT INTO untracked_review_result(request_id,material_hash,review,recorded_at)
VALUES(sqlc.arg(request_id)::uuid,sqlc.arg(material_hash)::text,sqlc.arg(review)::jsonb,sqlc.arg(recorded_at)::timestamptz);

-- name: FinishUntrackedReviewJob :execrows
UPDATE job SET state='succeeded',result=sqlc.arg(result)::jsonb,error=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=sqlc.arg(id)::uuid AND kind='untracked_review' AND state='running' AND lease_owner=sqlc.arg(owner)::text
AND lease_token=sqlc.arg(token)::bigint AND lease_until>clock_timestamp();

-- name: GetUntrackedConfirmationKey :one
SELECT to_jsonb(c)::jsonb AS document FROM untracked_review_confirmation c
WHERE scope_id=sqlc.arg(scope_id)::uuid AND file_id=sqlc.arg(file_id)::uuid AND command_key=sqlc.arg(command_key)::text;

-- name: InsertUntrackedConfirmation :exec
INSERT INTO untracked_review_confirmation(id,owner_id,scope_id,file_id,review_id,command_key,input_hash,receipt)
VALUES(sqlc.arg(id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(scope_id)::uuid,sqlc.arg(file_id)::uuid,
sqlc.arg(review_id)::uuid,sqlc.arg(command_key)::text,sqlc.arg(input_hash)::text,sqlc.arg(receipt)::jsonb);

-- Full rows, including original receipt bytes as JSON, job lease and both tails.
-- name: UntrackedReviewSeal :one
SELECT jsonb_build_array(
 (SELECT to_jsonb(r) FROM untracked_review_request r WHERE r.id=sqlc.arg(request_id)::uuid),
 (SELECT to_jsonb(r) FROM untracked_review_request_receipt r WHERE r.request_id=sqlc.arg(request_id)::uuid),
 (SELECT to_jsonb(r) FROM untracked_review_result r WHERE r.request_id=sqlc.arg(request_id)::uuid),
 (SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]'::jsonb) FROM untracked_review_confirmation c WHERE c.review_id=sqlc.arg(request_id)::uuid),
 (SELECT to_jsonb(j) FROM job j JOIN untracked_review_request r ON r.job_id=j.id WHERE r.id=sqlc.arg(request_id)::uuid),
 (SELECT to_jsonb(a) FROM audit_log a WHERE a.id=sqlc.arg(audit_id)::uuid),
 (SELECT to_jsonb(e) FROM outbox_event e WHERE e.id=sqlc.arg(event_id)::uuid)
)::jsonb AS document;

-- R0 only: never grants any filesystem or publication authority.
-- name: RecoverUntrackedReviewJobs :many
WITH expired AS (
 SELECT id FROM job WHERE kind='untracked_review' AND max_attempts=3 AND state='running' AND lease_until<=clock_timestamp()
 ORDER BY lease_until,id LIMIT sqlc.arg(page_size)::integer FOR UPDATE SKIP LOCKED
)
UPDATE job j SET state=CASE WHEN attempts>=3 THEN 'dead' ELSE 'queued' END,
 lease_owner=NULL,lease_until=NULL,finished_at=CASE WHEN attempts>=3 THEN clock_timestamp() ELSE NULL END,updated_at=clock_timestamp()
FROM expired e WHERE j.id=e.id AND j.kind='untracked_review' AND j.state='running' AND j.lease_until<=clock_timestamp()
RETURNING j.id;
