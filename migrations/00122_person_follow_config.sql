-- +goose Up
CREATE TABLE person_follow_config (
 id UUID PRIMARY KEY CHECK (id <> '00000000-0000-0000-0000-000000000000'),
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 person_id BIGINT NOT NULL CHECK(person_id > 0),
 role TEXT NOT NULL CHECK(role IN ('actor','director')),
 enabled BOOLEAN NOT NULL,
 min_release_date DATE NOT NULL CHECK(min_release_date BETWEEN DATE '1800-01-01' AND DATE '9999-12-31'),
 interval_minutes INTEGER NOT NULL CHECK(interval_minutes BETWEEN 15 AND 10080),
 movie_root_id UUID REFERENCES library_root(id) ON DELETE RESTRICT,
 tv_root_id UUID REFERENCES library_root(id) ON DELETE RESTRICT,
 quality_policy_id TEXT NOT NULL,
 quality_policy_revision BIGINT NOT NULL CHECK(quality_policy_revision > 0),
 bindings_configured BOOLEAN NOT NULL,
 downloader_id UUID REFERENCES download_client(id) ON DELETE RESTRICT,
 version BIGINT NOT NULL CHECK(version >= 1),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(owner_id,person_id,role),
 UNIQUE(id,owner_id),
 FOREIGN KEY(quality_policy_id,quality_policy_revision) REFERENCES policy_revision(policy_id,revision) ON DELETE RESTRICT,
 CHECK(NOT enabled OR movie_root_id IS NOT NULL OR tv_root_id IS NOT NULL),
 CHECK(movie_root_id IS NULL OR tv_root_id IS NULL OR movie_root_id <> tv_root_id),
 CHECK(bindings_configured OR downloader_id IS NULL),
 CHECK(updated_at >= created_at)
);
CREATE INDEX person_follow_owner_idx ON person_follow_config(owner_id,created_at DESC,id);
CREATE TABLE person_follow_indexer (
 config_id UUID NOT NULL REFERENCES person_follow_config(id) ON DELETE RESTRICT,
 indexer_id UUID NOT NULL REFERENCES indexer(id) ON DELETE RESTRICT,
 PRIMARY KEY(config_id,indexer_id)
);
CREATE TABLE person_follow_config_history (
 config_id UUID NOT NULL,
 owner_id UUID NOT NULL,
 version BIGINT NOT NULL CHECK(version >= 1),
 command_key TEXT NOT NULL CHECK(octet_length(command_key) BETWEEN 22 AND 181 AND command_key LIKE 'person.follow.config:%'),
 request_hash TEXT NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 input JSONB NOT NULL CHECK(jsonb_typeof(input)='object'),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(config_id,version),
 UNIQUE(owner_id,command_key),
 FOREIGN KEY(config_id,owner_id) REFERENCES person_follow_config(id,owner_id) ON DELETE RESTRICT
);
-- +goose StatementBegin
CREATE FUNCTION protect_person_follow_config() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'UPDATE' THEN
  RAISE EXCEPTION 'person follow history cannot be removed' USING ERRCODE='55000';
 END IF;
 IF ROW(NEW.id,NEW.owner_id,NEW.person_id,NEW.role,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.owner_id,OLD.person_id,OLD.role,OLD.created_at)
  OR NEW.version <> OLD.version+1 OR NEW.updated_at <= OLD.updated_at THEN
  RAISE EXCEPTION 'person follow identity or version is immutable' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION protect_person_follow_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'person follow history is immutable' USING ERRCODE='55000'; END $$;
-- +goose StatementEnd
CREATE TRIGGER person_follow_config_change_guard BEFORE UPDATE OR DELETE ON person_follow_config FOR EACH ROW EXECUTE FUNCTION protect_person_follow_config();
CREATE TRIGGER person_follow_config_truncate_guard BEFORE TRUNCATE ON person_follow_config FOR EACH STATEMENT EXECUTE FUNCTION protect_person_follow_config();
CREATE TRIGGER person_follow_history_guard BEFORE UPDATE OR DELETE OR TRUNCATE ON person_follow_config_history FOR EACH STATEMENT EXECUTE FUNCTION protect_person_follow_history();
REVOKE ALL ON person_follow_config,person_follow_indexer,person_follow_config_history FROM PUBLIC;

-- +goose Down
LOCK TABLE person_follow_config,person_follow_indexer,person_follow_config_history IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM person_follow_config) OR EXISTS(SELECT 1 FROM person_follow_config_history) THEN
  RAISE EXCEPTION 'cannot discard person follow configuration history' USING ERRCODE='55000';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE person_follow_config_history;
DROP TABLE person_follow_indexer;
DROP TABLE person_follow_config;
DROP FUNCTION protect_person_follow_history();
DROP FUNCTION protect_person_follow_config();
