-- name: GetSubtitleCandidateFileRevision :one
SELECT mapping_revision FROM library_file WHERE id=sqlc.arg(file_id)::uuid AND retired_at IS NULL;

-- name: GetLatestSubtitleCandidateQuery :one
SELECT id FROM subtitle_candidate_query WHERE file_id=sqlc.arg(file_id)::uuid AND owner_id=sqlc.arg(owner_id)::uuid
ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: GetSubtitleCandidateState :one
SELECT c.file_id,c.mapping_revision,c.outcome,j.state AS job_state,
 COALESCE(j.lease_until>clock_timestamp() AND j.state='running',false)::boolean AS valid_lease
FROM subtitle_candidate_query c JOIN job j ON j.id=c.id WHERE c.id=sqlc.arg(query_id)::uuid;

-- name: ListSubtitleCandidateContents :many
SELECT id, content_encrypted FROM subtitle_candidate WHERE query_id=sqlc.arg(query_id)::uuid ORDER BY ordinal;

-- name: LockSubtitleCandidateCommand :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(command_scope)::text,0));

-- name: GetSubtitleCandidateCommand :one
SELECT id,file_id,mapping_revision FROM subtitle_candidate_query
WHERE owner_id=sqlc.arg(owner_id)::uuid AND command_key=sqlc.arg(command_key)::text;

-- name: FindSubtitleCandidateSource :one
SELECT e.id AS event_id,u.id AS unit_id
FROM library_file f JOIN library_file_unit fu ON fu.file_id=f.id AND fu.retired_at IS NULL
JOIN media_unit u ON u.id=fu.unit_id AND NOT u.retired
JOIN outbox_event e ON e.aggregate_type='media' AND e.aggregate_id=u.id::text AND e.event_type='media.imported'
 AND e.payload->>'file_id'=f.id::text AND e.payload->>'unit_id'=u.id::text
WHERE f.id=sqlc.arg(file_id)::uuid AND f.retired_at IS NULL AND f.mapping_revision=sqlc.arg(mapping_revision)::bigint
ORDER BY e.created_at DESC,e.id DESC,u.id LIMIT 1;

-- name: CreateSubtitleCandidateJob :exec
INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts,attempts,lease_owner,lease_token,lease_until)
VALUES(sqlc.arg(job_id)::uuid,'media_subtitle_candidates',sqlc.arg(owner)::text,'running',sqlc.arg(payload)::jsonb,1,1,sqlc.arg(owner)::text,1,clock_timestamp()+interval '2 minutes');

-- name: CreateSubtitleCandidateQuery :exec
INSERT INTO subtitle_candidate_query(id,owner_id,file_id,mapping_revision,command_key,outcome,snapshot)
VALUES(sqlc.arg(query_id)::uuid,sqlc.arg(owner_id)::uuid,sqlc.arg(file_id)::uuid,sqlc.arg(mapping_revision)::bigint,sqlc.arg(command_key)::text,'querying',sqlc.arg(snapshot)::jsonb);

-- name: LockSubtitleCandidateQuery :one
SELECT owner_id,snapshot FROM subtitle_candidate_query WHERE id=sqlc.arg(query_id)::uuid FOR UPDATE;

-- name: InsertSubtitleCandidate :exec
INSERT INTO subtitle_candidate(id,query_id,ordinal,content_encrypted)
VALUES(sqlc.arg(candidate_id)::uuid,sqlc.arg(query_id)::uuid,sqlc.arg(ordinal)::integer,sqlc.arg(content_encrypted)::bytea);

-- name: FinishSubtitleCandidateQuery :execrows
UPDATE subtitle_candidate_query SET outcome=sqlc.arg(outcome)::text
WHERE id=sqlc.arg(query_id)::uuid AND outcome='querying' AND NOT confirm_permit;

-- name: LockSubtitleCandidateConfirmation :one
SELECT c.id,c.owner_id,c.mapping_revision,c.snapshot,c.outcome,c.confirm_permit,s.content_encrypted
FROM subtitle_candidate_query c JOIN subtitle_candidate s ON s.query_id=c.id
WHERE s.id=sqlc.arg(candidate_id)::uuid AND c.file_id=sqlc.arg(file_id)::uuid FOR UPDATE OF c;

-- name: BeginSubtitleCandidateJobConfirmation :one
UPDATE job SET state='running',lease_owner=sqlc.arg(owner)::text,lease_token = 1 + lease_token,
 lease_until=clock_timestamp()+interval '2 minutes',finished_at=NULL
WHERE id=sqlc.arg(job_id)::uuid AND kind='media_subtitle_candidates' AND state='succeeded' RETURNING lease_token;

-- name: PermitSubtitleCandidateConfirmation :exec
UPDATE subtitle_candidate_query SET outcome='confirming',confirm_permit=true,selected_candidate_id=sqlc.arg(candidate_id)::uuid
WHERE id=sqlc.arg(query_id)::uuid;

-- name: FinishSubtitleCandidateConfirmation :execrows
UPDATE subtitle_candidate_query SET outcome=sqlc.arg(outcome)::text
WHERE id=sqlc.arg(query_id)::uuid AND outcome='confirming' AND confirm_permit;
