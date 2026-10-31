-- name: ListUntrackedReviewScopes :many
SELECT id
FROM untracked_directory_scope
WHERE owner_id=sqlc.arg(owner_id)::uuid
  AND id > sqlc.arg(after_id)::uuid
ORDER BY id
LIMIT 101;
