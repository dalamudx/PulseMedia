-- name: LockPersonFollowOwner :one
SELECT role,is_active FROM app_user WHERE id=$1 FOR UPDATE;

-- name: LockPersonFollowConfig :one
SELECT * FROM person_follow_config WHERE id=$1 FOR UPDATE;

-- name: ReadPersonFollowConfig :one
SELECT * FROM person_follow_config WHERE id=$1 AND owner_id=$2;

-- name: ListPersonFollowConfigs :many
SELECT * FROM person_follow_config WHERE owner_id=$1 ORDER BY created_at DESC,id LIMIT 101;

-- name: CountPersonFollowConfigs :one
SELECT count(*) FROM person_follow_config WHERE owner_id=$1;

-- name: ListPersonFollowIndexers :many
SELECT indexer_id FROM person_follow_indexer WHERE config_id=$1 ORDER BY indexer_id LIMIT 65;

-- name: ClearPersonFollowIndexers :exec
DELETE FROM person_follow_indexer WHERE config_id=$1;

-- name: AddPersonFollowIndexer :exec
INSERT INTO person_follow_indexer(config_id,indexer_id)VALUES($1,$2);

-- name: UpsertPersonFollowConfig :one
INSERT INTO person_follow_config(id,owner_id,person_id,role,enabled,min_release_date,interval_minutes,movie_root_id,tv_root_id,quality_policy_id,quality_policy_revision,bindings_configured,downloader_id,version)
VALUES(sqlc.arg(id),sqlc.arg(owner_id),sqlc.arg(person_id),sqlc.arg(role),sqlc.arg(enabled),sqlc.arg(min_release_date),sqlc.arg(interval_minutes),sqlc.arg(movie_root_id),sqlc.arg(tv_root_id),sqlc.arg(quality_policy_id),sqlc.arg(quality_policy_revision),sqlc.arg(bindings_configured),sqlc.arg(downloader_id),1)
ON CONFLICT(id) DO UPDATE SET
 enabled=EXCLUDED.enabled,min_release_date=EXCLUDED.min_release_date,interval_minutes=EXCLUDED.interval_minutes,
 movie_root_id=EXCLUDED.movie_root_id,tv_root_id=EXCLUDED.tv_root_id,quality_policy_id=EXCLUDED.quality_policy_id,quality_policy_revision=EXCLUDED.quality_policy_revision,
 bindings_configured=EXCLUDED.bindings_configured,downloader_id=EXCLUDED.downloader_id,version=person_follow_config.version+1,updated_at=clock_timestamp()
WHERE person_follow_config.owner_id=sqlc.arg(owner_id) AND person_follow_config.version=sqlc.arg(expected_version)::bigint
RETURNING *;

-- name: CreatePersonFollowHistory :exec
INSERT INTO person_follow_config_history(config_id,owner_id,version,command_key,request_hash,input,snapshot)
VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: ReadPersonFollowHistory :one
SELECT * FROM person_follow_config_history WHERE owner_id=$1 AND command_key=$2;
