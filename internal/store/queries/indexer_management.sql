-- name: IndexerLifecycleSchemaAvailable :one
SELECT to_regclass('indexer_lifecycle') IS NOT NULL AS available;

-- name: IndexerLifecycleAvailable :one
SELECT COALESCE((SELECT enabled AND deleted_at IS NULL FROM indexer_lifecycle WHERE indexer_id=$1), true)::boolean AS available;

-- name: CreateIndexerLifecycle :exec
INSERT INTO indexer_lifecycle(indexer_id) VALUES($1);

-- name: ListManagedIndexerIDs :many
SELECT i.id FROM indexer i LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id
WHERE l.deleted_at IS NULL ORDER BY i.created_at,i.id;

-- name: UpdateManagedIndexerConfiguration :execrows
UPDATE indexer_configuration SET protocol=$2, url_encrypted=$3,
 behavior_version='explicit-v1', configuration_version=$4
WHERE indexer_id=$1 AND configuration_version < $4
 AND EXISTS(SELECT 1 FROM indexer i WHERE i.id=$1 AND i.config_version=$4);

-- name: TombstoneManagedIndexer :execrows
UPDATE indexer_lifecycle SET enabled=false,deleted_at=statement_timestamp()
WHERE indexer_id=$1 AND deleted_at IS NULL;

-- name: EnsureIndexerLifecycle :exec
INSERT INTO indexer_lifecycle(indexer_id) VALUES($1) ON CONFLICT DO NOTHING;

-- name: MarkManagedIndexerOffline :exec
UPDATE indexer SET health='offline',rss_enabled=false WHERE id=$1;
