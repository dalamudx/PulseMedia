-- +goose Up
CREATE TABLE indexer_lifecycle (
    indexer_id UUID PRIMARY KEY REFERENCES indexer(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT true,
    deleted_at TIMESTAMPTZ,
    CHECK (deleted_at IS NULL OR NOT enabled)
);
INSERT INTO indexer_lifecycle(indexer_id) SELECT id FROM indexer;

-- +goose StatementBegin
CREATE FUNCTION guard_indexer_management_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.configuration_version < OLD.configuration_version
       OR ((NEW.protocol, NEW.url_encrypted, NEW.behavior_version) IS DISTINCT FROM
           (OLD.protocol, OLD.url_encrypted, OLD.behavior_version)
           AND NEW.configuration_version <= OLD.configuration_version)
       OR (NEW.configuration_version <> OLD.configuration_version AND
           NEW.configuration_version <> (SELECT config_version FROM indexer WHERE id = NEW.indexer_id))
       OR NEW.indexer_id <> OLD.indexer_id THEN
        RAISE EXCEPTION 'indexer configuration version conflict';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER indexer_management_configuration_guard BEFORE UPDATE ON indexer_configuration
FOR EACH ROW EXECUTE FUNCTION guard_indexer_management_configuration();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM indexer_lifecycle WHERE NOT enabled OR deleted_at IS NOT NULL)
       OR EXISTS (SELECT 1 FROM audit_log WHERE action IN ('indexer.configuration_created','indexer.configuration_updated','indexer.configuration_deleted'))
       OR EXISTS (SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'indexer.create:%' OR idempotency_key LIKE 'indexer.update:%' OR idempotency_key LIKE 'indexer.delete:%')
       OR EXISTS (SELECT 1 FROM outbox_event WHERE event_type IN ('indexer.configuration_created','indexer.configuration_updated','indexer.configuration_deleted')) THEN
        RAISE EXCEPTION 'indexer management history downgrade refused';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER indexer_management_configuration_guard ON indexer_configuration;
DROP FUNCTION guard_indexer_management_configuration();
DROP TABLE indexer_lifecycle;
