-- name: ListLibraryCatalog :many
SELECT f.id AS file_id,u.id AS unit_id,m.id AS media_id,f.root_id,m.title,m.year,m.media_type,u.unit_key,r.name AS root_name,r.health AS root_health,f.rel_path,f.size_bytes,
EXISTS(SELECT 1 FROM unit_fact uf WHERE uf.source_file_id=f.id AND uf.unit_id=u.id AND uf.root_id=r.id AND uf.stage='imported' AND uf.revoked_at IS NULL AND (uf.expires_at IS NULL OR uf.expires_at>statement_timestamp())) AS imported,
EXISTS(SELECT 1 FROM unit_fact uf WHERE uf.source_file_id=f.id AND uf.unit_id=u.id AND uf.root_id=r.id AND uf.stage='confirmed' AND uf.revoked_at IS NULL AND (uf.expires_at IS NULL OR uf.expires_at>statement_timestamp())) AS confirmed
FROM library_file f JOIN library_file_unit fu ON fu.file_id=f.id AND fu.retired_at IS NULL JOIN media_unit u ON u.id=fu.unit_id AND NOT u.retired JOIN media m ON m.id=u.media_id JOIN library_root r ON r.id=f.root_id
WHERE f.retired_at IS NULL AND (f.id,u.id)>(sqlc.arg(after_file)::uuid,sqlc.arg(after_unit)::uuid)
ORDER BY f.id,u.id LIMIT sqlc.arg(row_limit)::integer;
