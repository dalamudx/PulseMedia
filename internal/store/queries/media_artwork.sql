-- name: UpsertCreationMediaArtwork :execrows
INSERT INTO media_artwork (
    media_id, provider, namespace, external_id, metadata_version, poster_path
)
SELECT m.id, 'tmdb', i.namespace, i.external_id, m.metadata_version, sqlc.arg(poster_path)
FROM media m
JOIN media_identity i ON i.media_id = m.id
WHERE m.id = sqlc.arg(media_id)
  AND m.media_type = sqlc.arg(media_type)
  AND i.namespace = sqlc.arg(namespace)
  AND i.namespace = 'tmdb.' || m.media_type
  AND i.external_id = sqlc.arg(external_id)
  AND (SELECT count(*) FROM media_identity matching
       WHERE matching.media_id = m.id AND matching.namespace = i.namespace) = 1
ON CONFLICT (media_id) DO UPDATE SET
    provider = EXCLUDED.provider,
    namespace = EXCLUDED.namespace,
    external_id = EXCLUDED.external_id,
    metadata_version = EXCLUDED.metadata_version,
    poster_path = EXCLUDED.poster_path,
    recorded_at = statement_timestamp();
