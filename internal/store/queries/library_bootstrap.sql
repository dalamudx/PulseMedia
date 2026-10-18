-- name: ListConfiguredRootMediaTypes :many
SELECT DISTINCT media_type FROM library_root;

-- name: HasConfiguredRootForType :one
SELECT EXISTS(SELECT 1 FROM library_root WHERE media_type = $1)::boolean;

-- name: LockLibraryRootBootstrap :exec
SELECT pg_advisory_xact_lock(hashtextextended('library-root-bootstrap', 0));
