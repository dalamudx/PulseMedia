-- name: LockSubscriptionListAccount :one
SELECT id, is_active, role FROM app_user WHERE id = $1 FOR SHARE;

-- name: SetSubscriptionListReadOnly :exec
SET TRANSACTION READ ONLY;

-- name: CountSubscriptionList :one
SELECT count(*)::bigint AS total,
 count(*) FILTER (WHERE m.media_type='movie')::bigint AS movies,
 count(*) FILTER (WHERE m.media_type='tv')::bigint AS tv
FROM subscription s JOIN media m ON m.id=s.media_id JOIN library_root r ON r.id=s.root_id
WHERE (sqlc.arg(is_admin)::boolean OR s.user_id=sqlc.arg(user_id)::uuid)
 AND (NOT sqlc.arg(own_only)::boolean OR s.user_id=sqlc.arg(user_id)::uuid)
 AND (sqlc.arg(search)::text='' OR strpos(lower(m.title),lower(sqlc.arg(search)))>0 OR strpos(s.id::text,lower(sqlc.arg(search)))>0 OR (sqlc.arg(is_admin)::boolean AND strpos(lower(r.path),lower(sqlc.arg(search)))>0))
 AND (sqlc.arg(tmdb_id)::text='' OR (SELECT CASE WHEN count(*)=1 THEN min(external_id) ELSE '' END FROM media_identity WHERE media_id=m.id AND namespace='tmdb.'||m.media_type)=sqlc.arg(tmdb_id));

-- name: ReadSubscriptionCalendarPage :one
WITH matched AS MATERIALIZED (
 SELECT w.id, w.subscription_id, u.air_date, u.unit_key
 FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id
 JOIN subscription s ON s.id=w.subscription_id JOIN media m ON m.id=s.media_id
 WHERE (sqlc.arg(is_admin)::boolean OR s.user_id=sqlc.arg(user_id)::uuid)
 AND m.media_type='tv' AND s.state IN ('active','paused') AND NOT u.retired AND NOT w.scope_excluded
 AND (CASE WHEN sqlc.arg(unknown_dates)::boolean THEN u.air_date IS NULL ELSE u.air_date BETWEEN sqlc.arg(start_date)::date AND sqlc.arg(end_date)::date END)
), page AS (
 SELECT * FROM matched ORDER BY air_date NULLS LAST, subscription_id, unit_key, id
 LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer
)
SELECT ARRAY(SELECT id FROM page)::uuid[] AS wanted_ids,
 ARRAY(SELECT DISTINCT subscription_id FROM page)::uuid[] AS subscription_ids,
 (SELECT count(*) FROM matched)::bigint AS total;

-- name: ReadSubscriptionListEntries :many
WITH page AS MATERIALIZED (
 SELECT s.id
 FROM subscription s JOIN media m ON m.id=s.media_id JOIN library_root r ON r.id=s.root_id
 LEFT JOIN LATERAL (
  SELECT CASE WHEN count(*)=1 THEN min(external_id) ELSE '' END AS external_id
  FROM media_identity WHERE media_id=m.id AND namespace='tmdb.'||m.media_type
 ) identity ON true
 WHERE (sqlc.arg(is_admin)::boolean OR s.user_id=sqlc.arg(user_id)::uuid)
 AND (NOT sqlc.arg(own_only)::boolean OR s.user_id=sqlc.arg(user_id)::uuid)
 AND (sqlc.arg(search)::text='' OR strpos(lower(m.title),lower(sqlc.arg(search)))>0 OR strpos(s.id::text,lower(sqlc.arg(search)))>0 OR (sqlc.arg(is_admin)::boolean AND strpos(lower(r.path),lower(sqlc.arg(search)))>0))
 AND (sqlc.arg(tmdb_id)::text='' OR identity.external_id=sqlc.arg(tmdb_id))
 AND (sqlc.arg(media_type)::text='' OR m.media_type=sqlc.arg(media_type))
 AND (NOT sqlc.arg(calendar)::boolean OR s.id=ANY(sqlc.arg(subscription_ids)::uuid[]))
 ORDER BY s.created_at DESC,s.id DESC
 LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer
)
SELECT s.id,s.user_id,s.media_id,s.root_id,s.quality_policy_id,s.quality_policy_revision,
 s.state,s.intent_version,s.scope_type,s.indexer_ids,s.downloader_id,
 m.title AS media_title,m.year AS media_year,m.media_type,r.name AS root_name,r.path AS root_path,
 COALESCE(identity.external_id,'')::text AS tmdb_external_id,
 CASE WHEN artwork.provider='tmdb' AND artwork.namespace='tmdb.'||m.media_type
 AND artwork.metadata_version=m.metadata_version AND artwork.external_id=identity.external_id
 THEN artwork.poster_path ELSE '' END::text AS poster_path,
 (SELECT count(*) FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id WHERE w.subscription_id=s.id AND NOT w.is_skipped AND NOT w.scope_excluded AND NOT u.retired)::bigint AS total_episodes,
 (SELECT count(*) FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id JOIN wanted_projection_validity p ON p.id=w.id WHERE w.subscription_id=s.id AND NOT w.is_skipped AND NOT w.scope_excluded AND NOT u.retired AND w.is_satisfied AND p.inputs_current AND w.next_evaluate_at>transaction_timestamp() AND (w.projection_valid_until IS NULL OR w.projection_valid_until>transaction_timestamp()))::bigint AS satisfied_episodes
FROM page JOIN subscription s ON s.id=page.id JOIN media m ON m.id=s.media_id JOIN library_root r ON r.id=s.root_id
LEFT JOIN media_artwork artwork ON artwork.media_id=m.id
LEFT JOIN LATERAL (
 SELECT CASE WHEN count(*)=1 THEN min(external_id) ELSE '' END AS external_id
 FROM media_identity WHERE media_id=m.id AND namespace='tmdb.'||m.media_type
) identity ON true
ORDER BY s.created_at DESC,s.id DESC;

-- name: ReadSubscriptionListWanted :many
SELECT w.id,w.subscription_id,w.unit_id,w.is_skipped,
 CASE WHEN p.inputs_current AND w.next_evaluate_at>transaction_timestamp() AND (w.projection_valid_until IS NULL OR w.projection_valid_until>transaction_timestamp()) THEN w.is_satisfied ELSE false END AS is_satisfied,
 CASE WHEN p.inputs_current AND w.next_evaluate_at>transaction_timestamp() AND (w.projection_valid_until IS NULL OR w.projection_valid_until>transaction_timestamp()) THEN COALESCE(w.current_fact_stage,'') ELSE '' END AS current_fact_stage,
 u.unit_key,u.air_date,u.absolute_number,u.retired,u.metadata_version,w.user_skipped,w.scope_excluded
FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id JOIN wanted_projection_validity p ON p.id=w.id
WHERE w.id=ANY(sqlc.arg(wanted_ids)::uuid[])
ORDER BY w.subscription_id,u.unit_key,w.id;
