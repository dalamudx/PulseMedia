-- name: IndexerConfigurationSchemaAvailable :one
SELECT to_regclass('indexer_configuration') IS NOT NULL AS available;

-- name: GetIndexerConfiguration :one
SELECT * FROM indexer_configuration WHERE indexer_id = $1;

-- name: ListIndexerConfigurationIDs :many
SELECT id FROM indexer ORDER BY id;

-- name: LockIndexerConfigurationBase :one
SELECT * FROM indexer WHERE id = $1 FOR UPDATE;

-- name: InsertIndexerConfiguration :exec
INSERT INTO indexer_configuration (indexer_id, protocol, url_encrypted, behavior_version, configuration_version)
VALUES ($1, $2, $3, $4, $5);

-- name: SetIndexerConfigurationOrigin :exec
UPDATE indexer SET url = $2 WHERE id = $1;

-- name: AuditIndexerConfigurationEncrypted :exec
INSERT INTO audit_log (id, actor_type, actor_id, action, target_type, target_id, payload)
VALUES ($1, 'system', 'system', 'indexer.configuration.encrypted', 'indexer', $2, '{}'::jsonb);
