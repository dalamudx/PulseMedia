-- +goose Up
CREATE TABLE douban_sync_config (
 id UUID PRIMARY KEY CHECK(id <> '00000000-0000-0000-0000-000000000000'),
 owner_id UUID NOT NULL UNIQUE REFERENCES app_user(id) ON DELETE RESTRICT,
 douban_uid TEXT NOT NULL CHECK(douban_uid COLLATE "C" ~ '^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$'),
 enabled BOOLEAN NOT NULL,
 movie_root_id UUID REFERENCES library_root(id) ON DELETE RESTRICT,
 tv_root_id UUID REFERENCES library_root(id) ON DELETE RESTRICT,
 version BIGINT NOT NULL CHECK(version >= 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(NOT enabled OR movie_root_id IS NOT NULL OR tv_root_id IS NOT NULL),
 UNIQUE(id,owner_id)
);
-- Each immutable row is the original command and its historical configuration.
CREATE TABLE douban_sync_config_receipt (
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 command_key TEXT NOT NULL CHECK(command_key COLLATE "C" ~ '^douban[.]sync[.]config:[A-Za-z0-9:_.-]+$' AND octet_length(command_key) BETWEEN 20 AND 179),
 config_id UUID NOT NULL,
 version BIGINT NOT NULL CHECK(version >= 1),
 request_hash TEXT NOT NULL CHECK(request_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 input JSONB NOT NULL,
 snapshot JSONB NOT NULL,
 PRIMARY KEY(owner_id,command_key),
 UNIQUE(config_id,version),
 FOREIGN KEY(config_id,owner_id) REFERENCES douban_sync_config(id,owner_id) ON DELETE RESTRICT
);
-- +goose StatementBegin
CREATE FUNCTION pulse_douban_sync_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'douban configuration history is protected'; END $$;
-- +goose StatementEnd
CREATE TRIGGER douban_sync_config_history_guard BEFORE DELETE OR TRUNCATE ON douban_sync_config FOR EACH STATEMENT EXECUTE FUNCTION pulse_douban_sync_history_guard();
CREATE TRIGGER douban_sync_receipt_history_guard BEFORE UPDATE OR DELETE OR TRUNCATE ON douban_sync_config_receipt FOR EACH STATEMENT EXECUTE FUNCTION pulse_douban_sync_history_guard();
REVOKE ALL ON douban_sync_config,douban_sync_config_receipt FROM PUBLIC;

-- +goose Down
LOCK TABLE douban_sync_config,douban_sync_config_receipt IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM douban_sync_config) OR EXISTS(SELECT 1 FROM douban_sync_config_receipt) THEN
  RAISE EXCEPTION 'cannot drop douban configuration history';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE douban_sync_config_receipt;
DROP TABLE douban_sync_config;
DROP FUNCTION pulse_douban_sync_history_guard();
