-- +goose Up
-- Keep the historical indexer row shape stable for snapshot readers.
CREATE TABLE indexer_configuration (
    indexer_id UUID PRIMARY KEY REFERENCES indexer(id) ON DELETE CASCADE,
    protocol TEXT NOT NULL CHECK (protocol IN ('torznab', 'nexusphp', 'rss')),
    url_encrypted BYTEA NOT NULL CHECK (octet_length(url_encrypted) > 0),
    behavior_version TEXT NOT NULL CHECK (behavior_version IN ('legacy-v1', 'explicit-v1')),
    configuration_version BIGINT NOT NULL CHECK (configuration_version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- +goose Down
-- Deleting a site must not erase evidence that plaintext downgrade is unsafe.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM indexer_configuration)
       OR EXISTS (SELECT 1 FROM audit_log WHERE action = 'indexer.configuration.encrypted') THEN
        RAISE EXCEPTION 'encrypted indexer configuration downgrade refused';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE indexer_configuration;
