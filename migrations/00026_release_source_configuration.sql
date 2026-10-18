-- +goose Up
-- Keep frozen encrypted references from each source configuration. An old guid
-- must not silently supply yesterday's credential/link to a new execution.
ALTER TABLE release_candidate ADD COLUMN source_config_version BIGINT NOT NULL DEFAULT 0 CHECK(source_config_version>=0);
ALTER TABLE release_candidate DROP CONSTRAINT release_candidate_indexer_guid_uniq;
ALTER TABLE release_candidate ADD CONSTRAINT release_candidate_source_guid_uniq UNIQUE(indexer_id,guid,source_config_version);
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM release_candidate GROUP BY indexer_id,guid HAVING count(*)>1) THEN RAISE EXCEPTION 'cannot downgrade versioned source history without losing references'; END IF;END $$;
-- +goose StatementEnd
ALTER TABLE release_candidate DROP CONSTRAINT release_candidate_source_guid_uniq;
ALTER TABLE release_candidate DROP COLUMN source_config_version;
ALTER TABLE release_candidate ADD CONSTRAINT release_candidate_indexer_guid_uniq UNIQUE(indexer_id,guid);
