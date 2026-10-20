-- +goose Up
CREATE TABLE site_account_stats_config (
 indexer_id UUID PRIMARY KEY REFERENCES indexer(id) ON DELETE RESTRICT,
 expected_account_id TEXT NOT NULL CHECK(expected_account_id COLLATE "C" ~ '^[1-9][0-9]{0,19}$' AND expected_account_id::numeric <= 18446744073709551615),
 profile TEXT NOT NULL CHECK(profile='nexusphp_classic_info_block_v1'),
 enabled BOOLEAN NOT NULL,
 interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 900 AND 86400),
 version BIGINT NOT NULL CHECK(version >= 1),
 source_config_version BIGINT NOT NULL CHECK(source_config_version >= 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE site_account_stats_config_receipt (
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 indexer_id UUID NOT NULL REFERENCES site_account_stats_config(indexer_id) ON DELETE RESTRICT,
 command_key TEXT NOT NULL CHECK(command_key COLLATE "C" ~ '^[A-Za-z0-9:_.-]{1,160}$'),
 version BIGINT NOT NULL CHECK(version >= 1),
 request_hash TEXT NOT NULL CHECK(request_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 input JSONB NOT NULL,
 snapshot JSONB NOT NULL,
 PRIMARY KEY(owner_id,indexer_id,command_key),
 UNIQUE(indexer_id,version)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_site_account_stats_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'site account configuration history is protected'; END $$;
-- +goose StatementEnd
CREATE TRIGGER site_account_stats_config_history_guard BEFORE DELETE OR TRUNCATE ON site_account_stats_config FOR EACH STATEMENT EXECUTE FUNCTION pulse_site_account_stats_history_guard();
CREATE TRIGGER site_account_stats_receipt_history_guard BEFORE UPDATE OR DELETE OR TRUNCATE ON site_account_stats_config_receipt FOR EACH STATEMENT EXECUTE FUNCTION pulse_site_account_stats_history_guard();
REVOKE ALL ON site_account_stats_config,site_account_stats_config_receipt FROM PUBLIC;

-- +goose Down
LOCK TABLE site_account_stats_config,site_account_stats_config_receipt IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM site_account_stats_config) OR EXISTS(SELECT 1 FROM site_account_stats_config_receipt) THEN
 RAISE EXCEPTION 'cannot drop site account configuration history';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE site_account_stats_config_receipt;
DROP TABLE site_account_stats_config;
DROP FUNCTION pulse_site_account_stats_history_guard();
