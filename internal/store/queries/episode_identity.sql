-- name: BindTMDBEpisodeIdentity :execrows
INSERT INTO media_unit_identity(unit_id,namespace,external_id,tmdb_series_id) VALUES($1,'tmdb.episode',$2,$3) ON CONFLICT DO NOTHING;
-- name: GetTMDBEpisodeIdentityForUnit :one
SELECT * FROM media_unit_identity WHERE unit_id=$1 AND namespace='tmdb.episode';
-- name: ListTMDBEpisodeUnits :many
SELECT u.*,i.external_id AS tmdb_episode_id,i.tmdb_series_id FROM media_unit u LEFT JOIN media_unit_identity i ON i.unit_id=u.id AND i.namespace='tmdb.episode' WHERE u.media_id=$1 ORDER BY u.id;
