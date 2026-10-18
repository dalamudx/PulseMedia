-- name: ResolveImportedRefreshFile :one
SELECT f.*,r.path AS root_path,r.health AS root_health
FROM library_file f JOIN library_root r ON r.id=f.root_id
WHERE f.id=sqlc.arg(file_id) AND f.retired_at IS NULL
 AND EXISTS (SELECT 1 FROM library_file_unit fu WHERE fu.file_id=f.id AND fu.unit_id=sqlc.arg(unit_id) AND fu.retired_at IS NULL)
 AND EXISTS (SELECT 1 FROM unit_fact uf WHERE uf.source_file_id=f.id AND uf.unit_id=sqlc.arg(unit_id)
  AND uf.root_id=f.root_id AND uf.stage='imported' AND uf.revoked_at IS NULL);

-- name: LockMediaConfirmationServer :one
SELECT * FROM media_server WHERE id=$1 FOR SHARE;

-- name: GetCurrentMediaConfirmation :one
SELECT * FROM unit_fact WHERE unit_id=sqlc.arg(unit_id) AND source_file_id=sqlc.arg(file_id) AND root_id=sqlc.arg(root_id) AND stage='confirmed' AND revoked_at IS NULL ORDER BY created_at,id LIMIT 1;

