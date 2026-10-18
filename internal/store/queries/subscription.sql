-- name: InitializeSubscriptionTVScope :one
UPDATE subscription SET seasons=$2,include_specials=$3 WHERE id=$1 AND intent_version=1 RETURNING *;

-- name: ReconfigureSubscription :one
UPDATE subscription SET quality_policy_id=$2,quality_policy_revision=$3,indexer_ids=$4,downloader_id=$5,scope_type=$6,seasons=$7,include_specials=$8,intent_version=intent_version+1,updated_at=statement_timestamp() WHERE id=$1 RETURNING *;

-- name: SkipWantedBeforeReconfiguration :exec
UPDATE wanted_item SET scope_excluded=true,is_skipped=true,intent_version=$2,next_evaluate_at=statement_timestamp() WHERE subscription_id=$1;

-- Subscription & Wanted Queries

-- name: CreateSubscription :one
INSERT INTO subscription (
    id, user_id, media_id, root_id, quality_policy_id, quality_policy_revision,
    indexer_ids, downloader_id, state, intent_version, scope_type, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, 'active', 1, $9, statement_timestamp(), statement_timestamp()
) RETURNING *;

-- name: GetSubscriptionByID :one
SELECT * FROM subscription WHERE id = $1;

-- name: ListSubscriptionsByUser :many
SELECT * FROM subscription
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: ListAllSubscriptions :many
SELECT * FROM subscription
ORDER BY created_at DESC;

-- name: UpdateSubscriptionState :one
UPDATE subscription
SET state = $2, intent_version = intent_version + 1, updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;


-- name: GetSubscriptionByUserMediaRoot :one
SELECT * FROM subscription
WHERE user_id = $1 AND media_id = $2 AND root_id = $3
LIMIT 1;

-- name: DeleteSubscription :exec
DELETE FROM subscription WHERE id = $1;

-- Wanted Items
-- name: UpsertWantedItem :one
INSERT INTO wanted_item (
    id, subscription_id, unit_id, intent_version, is_skipped,
    is_satisfied, next_evaluate_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, false, $6, statement_timestamp(), statement_timestamp()
) ON CONFLICT (subscription_id, unit_id) DO UPDATE
SET intent_version = EXCLUDED.intent_version,
    is_skipped = EXCLUDED.is_skipped,
    next_evaluate_at=EXCLUDED.next_evaluate_at,
    updated_at = statement_timestamp()
RETURNING *;

-- name: ListWantedItemsForSubscription :many
SELECT * FROM wanted_item
WHERE subscription_id = $1
ORDER BY created_at ASC;

-- name: SetWantedItemSkipped :one
UPDATE wanted_item
SET is_skipped = $2, updated_at = statement_timestamp()
WHERE subscription_id = $1 AND unit_id = $3
RETURNING *;

-- name: UpdateWantedItemProjection :one
UPDATE wanted_item
SET current_fact_stage = $2, is_satisfied = $3,
    last_evaluated_at = statement_timestamp(), next_evaluate_at = $4,
    updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;

-- name: ListPendingEvaluationWantedItems :many
SELECT w.* FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id JOIN wanted_projection_validity p ON p.id=w.id
WHERE NOT w.is_skipped AND s.state='active'
 AND (NOT p.inputs_current OR w.projection_valid_until<=clock_timestamp() OR w.next_evaluate_at IS NULL OR w.next_evaluate_at<=clock_timestamp() OR EXISTS (
 SELECT 1 FROM indexer i WHERE i.rss_enabled AND i.health='online' AND i.rss_last_success_at>w.rss_seen_at
 AND s.indexer_ids @> jsonb_build_array(i.id::text)))
 AND NOT EXISTS (
 SELECT 1 FROM movie_upgrade_authority a JOIN movie_upgrade_send_job b USING(operation_id)
 JOIN job j ON j.id=b.job_id
 WHERE a.subscription_id=w.subscription_id AND a.unit_id=w.unit_id
 AND a.status='reserved_not_executable'
 AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND b.protected_sha256=pulse_upgrade_send_job_protected(a.operation_id)
 AND pulse_is_upgrade_send_job(j)
 AND ((j.state='queued' AND j.attempts=0)
   OR (j.state='running' AND j.attempts=1 AND j.lease_until>clock_timestamp()))
 )
 AND NOT EXISTS (
 SELECT 1 FROM movie_upgrade_authority a JOIN movie_upgrade_preparation_lease l USING(operation_id)
 WHERE a.subscription_id=w.subscription_id AND a.unit_id=w.unit_id
 AND a.status='reserved_not_executable' AND l.actor_id=a.actor_id
 AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND l.state='leased' AND l.lease_until>clock_timestamp()
 )
 AND NOT EXISTS (
 SELECT 1 FROM movie_upgrade_authority a
 JOIN movie_upgrade_preparation p USING(operation_id)
 JOIN movie_upgrade_preparation_lease l USING(operation_id)
 JOIN download_task t ON t.id=p.task_id AND t.operation_id=a.operation_id
 JOIN subscription s ON s.id=a.subscription_id
 JOIN app_user u ON u.id=a.actor_id
 WHERE a.subscription_id=w.subscription_id AND a.unit_id=w.unit_id
 AND NOT w.is_skipped AND NOT w.scope_excluded
 AND a.status='reserved_not_executable' AND p.status='prepared_not_sent'
 AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND p.actor_id=a.actor_id AND l.actor_id=a.actor_id
 AND l.state='prepared' AND l.owner=p.owner AND l.token=p.token
 AND p.task_snapshot=to_jsonb(t)
 AND s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND a.snapshot->>'mode'=s.upgrade_mode
 AND s.intent_version=(a.snapshot->'observation'->>'intent_version')::bigint
 AND u.is_active AND u.role='admin'
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_send_job b WHERE b.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_send_authorization x WHERE x.operation_id=a.operation_id)
 )
 AND NOT EXISTS (
 SELECT 1 FROM wanted_item waiting
 JOIN movie_upgrade_authority a ON a.subscription_id=waiting.subscription_id AND a.unit_id=waiting.unit_id
 JOIN subscription s ON s.id=a.subscription_id
 JOIN app_user u ON u.id=a.actor_id
 JOIN acquisition_reservation lane ON lane.operation_id=a.operation_id AND lane.unit_id=a.unit_id
 JOIN unit_reservation reservation ON reservation.operation_id=a.operation_id AND reservation.unit_id=a.unit_id
 JOIN download_requirement demand ON demand.operation_id=a.operation_id AND demand.unit_id=a.unit_id
 WHERE waiting.id=w.id AND NOT waiting.is_skipped AND NOT waiting.scope_excluded
 AND a.status='reserved_not_executable' AND (a.snapshot->>'valid_until')::timestamptz>clock_timestamp()
 AND s.state='active' AND s.upgrade_mode IN ('stop_at_cutoff','replace_if_better') AND a.snapshot->>'mode'=s.upgrade_mode AND s.intent_version=a.intent_version
 AND s.root_id=a.root_id AND s.quality_policy_id=a.policy_id AND s.quality_policy_revision=a.policy_revision
 AND u.is_active AND u.role='admin'
 AND lane.released_at IS NULL AND lane.task_id IS NULL AND lane.candidate_id=a.candidate_id
 AND lane.source_root_id=a.root_id AND lane.source_generation=a.root_generation
 AND reservation.purpose='upgrade' AND reservation.state='reserved' AND reservation.root_id=a.root_id
 AND reservation.candidate_id=a.candidate_id AND (reservation.expires_at IS NULL OR reservation.expires_at>clock_timestamp())
 AND demand.state='pending' AND demand.released_at IS NULL AND demand.root_id=a.root_id AND demand.actor_id=a.actor_id::text
 AND demand.submission_fence->>'goal'='upgrade' AND demand.submission_fence->>'authority'=a.operation_id::text
 AND demand.submission_fence->>'protected_digest'=a.snapshot->>'protected_digest'
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_preparation_lease l WHERE l.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM movie_upgrade_preparation p WHERE p.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM download_task t WHERE t.operation_id=a.operation_id)
 AND NOT EXISTS (SELECT 1 FROM job j WHERE j.payload->>'operation_id'=a.operation_id::text)
 )
ORDER BY w.next_evaluate_at NULLS FIRST,w.id LIMIT $1;

-- name: HasFreshWantedRSSObservation :one
SELECT EXISTS(SELECT 1 FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id JOIN indexer i ON s.indexer_ids @> jsonb_build_array(i.id::text)
WHERE w.id=$1 AND i.rss_enabled AND i.health='online' AND i.rss_last_success_at>w.rss_seen_at) AS fresh;



-- name: GetActiveReservationInRoot :one
SELECT r.id, r.state, r.expires_at, r.operation_id
FROM unit_reservation r
WHERE r.unit_id = $1 AND r.root_id = $2 AND r.state IN ('reserved', 'dispatched')
LIMIT 1;

-- name: GetWantedItemByID :one
SELECT * FROM wanted_item WHERE id = $1;


-- name: UpdateSubscriptionTVScope :one
UPDATE subscription
SET scope_type = $2, seasons = $3, include_specials = $4,
    intent_version = intent_version + 1, updated_at = statement_timestamp()
WHERE id = $1
RETURNING *;


-- name: ListSubscriptionsWithMediaDetails :many
SELECT s.id, s.user_id, s.media_id, s.root_id, s.quality_policy_id, s.quality_policy_revision,
       s.state, s.intent_version, s.scope_type, s.created_at, s.updated_at, s.indexer_ids, s.downloader_id,
       m.title AS media_title, m.year AS media_year, m.media_type,
       r.name AS root_name, r.path AS root_path
FROM subscription s
JOIN media m ON s.media_id = m.id
JOIN library_root r ON s.root_id = r.id
ORDER BY s.created_at DESC;

-- name: ListSubscriptionsWithMediaDetailsByUser :many
SELECT s.id, s.user_id, s.media_id, s.root_id, s.quality_policy_id, s.quality_policy_revision,
       s.state, s.intent_version, s.scope_type, s.created_at, s.updated_at, s.indexer_ids, s.downloader_id,
       m.title AS media_title, m.year AS media_year, m.media_type,
       r.name AS root_name, r.path AS root_path
FROM subscription s
JOIN media m ON s.media_id = m.id
JOIN library_root r ON s.root_id = r.id
WHERE s.user_id = $1
ORDER BY s.created_at DESC;

-- name: ListWantedItemsWithUnitDetails :many
SELECT w.id, w.subscription_id, w.unit_id, w.is_skipped,w.user_skipped,w.scope_excluded,
 CASE WHEN p.inputs_current AND w.next_evaluate_at>clock_timestamp() AND (w.projection_valid_until IS NULL OR w.projection_valid_until>clock_timestamp()) THEN w.is_satisfied ELSE false END AS is_satisfied,
 CASE WHEN p.inputs_current AND w.next_evaluate_at>clock_timestamp() AND (w.projection_valid_until IS NULL OR w.projection_valid_until>clock_timestamp()) THEN COALESCE(w.current_fact_stage,'') ELSE '' END AS current_fact_stage,u.unit_key,u.air_date,u.absolute_number,u.retired,u.metadata_version
FROM wanted_item w
JOIN media_unit u ON w.unit_id = u.id JOIN wanted_projection_validity p ON p.id=w.id
WHERE w.subscription_id = $1
ORDER BY u.unit_key ASC;

-- name: GetWantedProjectionHorizon :one
SELECT min(t.valid_until)::timestamptz AS valid_until FROM (
 SELECT uf.expires_at AS valid_until FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
 JOIN unit_fact uf ON uf.unit_id=w.unit_id AND uf.root_id=s.root_id AND uf.revoked_at IS NULL
 WHERE w.id=$1 AND uf.expires_at>sqlc.arg(observed_at)::timestamptz
 UNION ALL SELECT (u.air_date::timestamp AT TIME ZONE 'UTC') FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE w.id=$1 AND (u.air_date::timestamp AT TIME ZONE 'UTC')>sqlc.arg(observed_at)::timestamptz
 UNION ALL SELECT r.deadline FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id JOIN search_round r ON r.wanted_item_id=w.id AND r.state='running' AND r.snapshot_intent_version=s.intent_version AND r.snapshot_policy_revision=s.quality_policy_revision WHERE w.id=$1 AND r.deadline>sqlc.arg(observed_at)::timestamptz
)t;

-- name: LockWantedProjection :one
SELECT * FROM wanted_item WHERE id=$1 FOR UPDATE;

-- name: WriteWantedProjection :exec
UPDATE wanted_item SET intent_version=$2,current_fact_stage=$3,is_satisfied=$4,next_evaluate_at=$5,last_evaluated_at=sqlc.arg(evaluated_at)::timestamptz,updated_at=clock_timestamp(),
 evaluated_intent_version=$2,evaluated_policy_id=sqlc.arg(policy_id),evaluated_policy_revision=sqlc.arg(policy_revision),
 evaluated_unit_id=sqlc.arg(unit_id),evaluated_media_id=sqlc.arg(media_id),evaluated_root_id=sqlc.arg(root_id),evaluated_root_generation=sqlc.arg(root_generation),evaluated_health_revision=sqlc.arg(health_revision),
 evaluated_media_metadata_version=sqlc.arg(media_metadata_version),evaluated_unit_metadata_version=sqlc.arg(unit_metadata_version),
 evaluated_facts_version=sqlc.arg(facts_version),evaluated_state=sqlc.arg(state),evaluated_skipped=is_skipped,projection_valid_until=sqlc.narg(valid_until)::timestamptz,
 evaluated_contract_version=sqlc.arg(contract_version)::text,evaluated_write_token=gen_random_uuid() WHERE id=$1;
