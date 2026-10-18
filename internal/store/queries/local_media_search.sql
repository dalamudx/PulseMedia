-- name: SearchLocalMedia :many
SELECT m.id, m.media_type, m.title, m.year,
       COALESCE(identity.external_id, '')::text AS tmdb_external_id
FROM media m
LEFT JOIN LATERAL (
    SELECT external_id FROM media_identity
    WHERE media_id = m.id AND namespace = 'tmdb.' || m.media_type
    ORDER BY external_id
    LIMIT 1
) identity ON true
WHERE m.media_type IN ('movie', 'tv')
  AND (sqlc.arg(media_kind)::text = 'multi' OR m.media_type = sqlc.arg(media_kind)::text)
  AND (sqlc.arg(release_year)::int = 0 OR m.year = sqlc.arg(release_year)::int)
  AND strpos(lower(m.title), lower(sqlc.arg(search_query)::text)) > 0
ORDER BY lower(m.title), m.year NULLS LAST, m.id
LIMIT 20;
