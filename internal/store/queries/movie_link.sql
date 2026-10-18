-- name: GetMovieLinkSnapshot :one
SELECT f.*,r.path AS root_path,r.media_type AS root_media_type,r.marker_filename,r.health,r.generation,r.health_revision,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('unit_id',fu.unit_id,'retired_at',fu.retired_at,'created_at',fu.created_at,'facts_version',u.facts_version,'unit_retired',u.retired) ORDER BY fu.unit_id)
 FROM library_file_unit fu JOIN media_unit u ON u.id=fu.unit_id WHERE fu.file_id=f.id),'[]'::jsonb)::text AS mapping_stamp
FROM library_file f JOIN library_root r ON r.id=f.root_id
WHERE f.root_id=$1 AND f.rel_path=$2 AND f.retired_at IS NULL;

-- name: LockMovieLinkIdentity :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(identity_key)::text,0));

-- name: GetMovieLinkFactState :one
SELECT count(*)::bigint AS history_count,
 count(*) FILTER(WHERE unit_id=sqlc.arg(unit_id) AND root_id=sqlc.arg(root_id) AND stage='imported' AND revoked_at IS NULL
 AND (expires_at IS NULL OR expires_at>clock_timestamp()) AND quality_profile=sqlc.arg(quality_json)::jsonb)::bigint AS replay_count
FROM unit_fact WHERE source_file_id=sqlc.arg(file_id);

-- name: StampMovieLinkedFile :exec
UPDATE library_file SET last_mutation_seq=$2 WHERE id=$1;
