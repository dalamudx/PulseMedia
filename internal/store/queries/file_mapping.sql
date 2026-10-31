-- name: ListFileMappingOptions :many
SELECT f.id,f.root_id,f.rel_path,r.name AS root_name,f.created_at
FROM library_file f JOIN library_root r ON r.id=f.root_id
WHERE f.retired_at IS NULL
  AND (sqlc.narg(root_id)::uuid IS NULL OR f.root_id=sqlc.narg(root_id)::uuid)
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
    OR f.created_at<sqlc.narg(before_created_at)::timestamptz
    OR (f.created_at=sqlc.narg(before_created_at)::timestamptz AND f.id>sqlc.arg(before_id)::uuid))
ORDER BY f.created_at DESC,f.id LIMIT 101;
-- name: ListFileMappingMediaOptions :many
SELECT id,title,media_type FROM media ORDER BY updated_at DESC,id LIMIT 100;

-- name: LockMappingFile :one
SELECT * FROM library_file WHERE id=$1 FOR NO KEY UPDATE;
-- name: LockMappingMedia :one
SELECT * FROM media WHERE id=$1 FOR NO KEY UPDATE;
-- name: ListFileMappingHistory :many
SELECT u.*,fu.retired_at AS mapping_retired_at FROM library_file_unit fu JOIN media_unit u ON u.id=fu.unit_id WHERE fu.file_id=$1 ORDER BY u.id;
-- name: ListFileMappingImportedEvidence :many
SELECT uf.* FROM unit_fact uf JOIN library_file_unit fu ON fu.file_id=uf.source_file_id AND fu.unit_id=uf.unit_id AND fu.retired_at IS NULL
WHERE uf.source_file_id=$1 AND uf.stage='imported' AND uf.revoked_at IS NULL AND (uf.expires_at IS NULL OR uf.expires_at>clock_timestamp()) ORDER BY uf.id;
-- name: RetireFileMappingsExcept :exec
UPDATE library_file_unit SET retired_at=clock_timestamp() WHERE file_id=$1 AND retired_at IS NULL AND NOT(unit_id=ANY($2::uuid[]));
-- name: SetMappingFileMutationSeq :exec
UPDATE library_file SET last_mutation_seq=$2 WHERE id=$1;
