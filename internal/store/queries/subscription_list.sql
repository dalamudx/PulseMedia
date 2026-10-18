-- name: LockSubscriptionListAccount :one
SELECT id, is_active, role FROM app_user WHERE id = $1 FOR SHARE;

-- PostgreSQL permits making this transaction read-only after the account lock.
-- The lock stays held through the REPEATABLE READ business snapshot.
-- name: SetSubscriptionListReadOnly :exec
SET TRANSACTION READ ONLY;

-- Keep the legacy list order and TMDB namespace/identity selection.
-- name: ReadSubscriptionListEntries :many
SELECT s.id, s.user_id, s.media_id, s.root_id, s.quality_policy_id, s.quality_policy_revision,
       s.state, s.intent_version, s.scope_type, s.indexer_ids, s.downloader_id,
       m.title AS media_title, m.year AS media_year, m.media_type,
       r.name AS root_name, r.path AS root_path,
       COALESCE(identity.external_id, '')::text AS tmdb_external_id
FROM subscription s
JOIN media m ON m.id = s.media_id
JOIN library_root r ON r.id = s.root_id
LEFT JOIN LATERAL (
    SELECT external_id FROM media_identity
    WHERE media_id = m.id AND namespace LIKE 'tmdb.%'
    LIMIT 1
) identity ON true
WHERE sqlc.arg(is_admin)::boolean OR s.user_id = sqlc.arg(user_id)::uuid
ORDER BY s.created_at DESC;

-- Apply exactly the same validity and expiry rules as ListWantedItemsWithUnitDetails.
-- name: ReadSubscriptionListWanted :many
SELECT w.id, w.subscription_id, w.unit_id, w.is_skipped,
       CASE WHEN p.inputs_current AND w.next_evaluate_at > clock_timestamp()
            AND (w.projection_valid_until IS NULL OR w.projection_valid_until > clock_timestamp())
            THEN w.is_satisfied ELSE false END AS is_satisfied,
       CASE WHEN p.inputs_current AND w.next_evaluate_at > clock_timestamp()
            AND (w.projection_valid_until IS NULL OR w.projection_valid_until > clock_timestamp())
            THEN COALESCE(w.current_fact_stage, '') ELSE '' END AS current_fact_stage,
       u.unit_key
FROM wanted_item w
JOIN media_unit u ON u.id = w.unit_id
JOIN wanted_projection_validity p ON p.id = w.id
WHERE w.subscription_id = ANY(sqlc.arg(subscription_ids)::uuid[])
ORDER BY w.subscription_id, u.unit_key ASC;
