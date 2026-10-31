-- name: SearchFileMappingMediaOptions :many
SELECT m.id,m.title,m.media_type,m.updated_at
FROM media m
WHERE (sqlc.arg(title_query)::text='' OR strpos(lower(m.title),lower(sqlc.arg(title_query)::text))>0 OR strpos(lower(coalesce(m.original_title,'')),lower(sqlc.arg(title_query)::text))>0)
  AND (sqlc.arg(media_type)::text='' OR m.media_type=sqlc.arg(media_type)::text)
  AND (sqlc.arg(tmdb_id)::bigint=0 OR EXISTS(SELECT 1 FROM media_identity i WHERE i.media_id=m.id AND i.namespace='tmdb.'||m.media_type AND i.external_id=sqlc.arg(tmdb_id)::text))
  AND (sqlc.narg(before_updated_at)::timestamptz IS NULL OR m.updated_at<sqlc.narg(before_updated_at)::timestamptz OR (m.updated_at=sqlc.narg(before_updated_at)::timestamptz AND m.id>sqlc.arg(before_id)::uuid))
ORDER BY m.updated_at DESC,m.id LIMIT 101;
