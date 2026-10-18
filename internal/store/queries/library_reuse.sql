-- name: ListLibraryReuseFiles :many
SELECT f.id AS file_id, f.root_id, f.rel_path, f.size_bytes, f.mtime_ns,
       f.file_fingerprint, f.mapping_revision, f.last_mutation_seq,
       r.name AS root_name, r.path AS root_path, r.media_type AS root_media_type,
       r.health, r.baseline_completed, r.generation, r.health_revision, r.marker_filename,
       COALESCE((SELECT jsonb_agg(jsonb_build_object(
           'unit_id', coverage.unit_id, 'unit_key', mu.unit_key, 'metadata_version', mu.metadata_version,
           'media_id', mu.media_id, 'retired', mu.retired
       ) ORDER BY coverage.unit_id)
       FROM library_file_unit coverage JOIN media_unit mu ON mu.id=coverage.unit_id
       WHERE coverage.file_id=f.id AND coverage.retired_at IS NULL), '[]'::jsonb)::jsonb AS unit_coverage,
       COALESCE((SELECT jsonb_agg(jsonb_build_object(
           'id', uf.id, 'unit_id', uf.unit_id, 'profile', uf.quality_profile, 'expires_at', uf.expires_at
       ) ORDER BY uf.id)
       FROM unit_fact uf
       WHERE uf.unit_id IN (SELECT unit_id FROM library_file_unit WHERE file_id=f.id AND retired_at IS NULL)
         AND uf.source_file_id=f.id AND uf.root_id=f.root_id
         AND uf.stage='imported' AND uf.revoked_at IS NULL
         AND (uf.expires_at IS NULL OR uf.expires_at>sqlc.arg(observed_at)::timestamptz)), '[]'::jsonb)::jsonb AS import_evidence
FROM library_file_unit fu
JOIN library_file f ON f.id=fu.file_id AND f.retired_at IS NULL
JOIN library_root r ON r.id=f.root_id
WHERE fu.unit_id=sqlc.arg(unit_id) AND fu.retired_at IS NULL
ORDER BY (f.root_id=sqlc.arg(target_root_id)) DESC, f.root_id, f.id
LIMIT 129;

-- name: GetLibraryReusePolicy :one
SELECT * FROM policy WHERE id=$1;

-- name: GetLibraryReuseUnit :one
SELECT * FROM media_unit WHERE id=$1;
