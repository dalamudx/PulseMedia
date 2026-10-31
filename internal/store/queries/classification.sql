-- name: ClassificationReadLatest :one
SELECT jsonb_build_object('version',version,'rules',rules,'published_at',published_at)::jsonb AS body FROM media_classification_policy ORDER BY version DESC LIMIT 1;
-- name: ClassificationReadVersion :one
SELECT jsonb_build_object('version',version,'rules',rules,'published_at',published_at)::jsonb AS body FROM media_classification_policy WHERE version=$1;
-- name: ClassificationLockPublish :exec
SELECT pg_advisory_xact_lock(hashtextextended('media.classification',0));
-- name: ClassificationLockSelection :exec
SELECT pg_advisory_xact_lock_shared(hashtextextended('media.classification',0));
-- name: ClassificationReadOriginal :one
SELECT version,request_hash FROM media_classification_policy WHERE actor_id=$1 AND request_key=$2;
-- name: ClassificationInsertPolicy :one
INSERT INTO media_classification_policy(version,rules,actor_id,request_key,request_hash) VALUES($1,$2,$3,$4,$5) RETURNING published_at;
-- name: ClassificationReadFacts :one
SELECT f.facts FROM media_classification_fact f JOIN media m ON m.id=f.media_id AND m.metadata_version=f.metadata_version WHERE f.media_id=$1;
-- name: ClassificationUpsertFacts :exec
INSERT INTO media_classification_fact(media_id,metadata_version,facts) SELECT id,metadata_version,sqlc.arg(facts)::jsonb FROM media WHERE id=sqlc.arg(media_id)::uuid ON CONFLICT(media_id) DO UPDATE SET metadata_version=EXCLUDED.metadata_version,facts=EXCLUDED.facts,observed_at=clock_timestamp();
-- name: ClassificationFreezeChoice :exec
INSERT INTO subscription_classification_choice(subscription_id,policy_version,root_id,root_generation,result,facts) VALUES($1,$2,$3,$4,$5,$6);
-- name: ClassificationFactsInstalled :one
SELECT (to_regclass('media_classification_fact') IS NOT NULL)::boolean AS installed;
-- name: ClassificationSnapshotTime :one
SELECT transaction_timestamp()::timestamptz AS observed_at;
