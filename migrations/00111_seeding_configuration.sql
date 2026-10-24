-- +goose Up
-- Eligibility configuration only: no scheduling or removal authorization.
CREATE TABLE seeding_configuration (
 client_id UUID PRIMARY KEY REFERENCES download_client(id) ON DELETE RESTRICT,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 version BIGINT NOT NULL CHECK(version>0),
 client_updated_at TIMESTAMPTZ NOT NULL,
 client_stamp TEXT NOT NULL CHECK(client_stamp COLLATE "C" ~ '^[0-9a-f]{64}$'),
 enabled BOOLEAN NOT NULL,
 interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 60 AND 86400),
 match_mode TEXT NOT NULL CHECK(match_mode IN ('any','all')),
 min_seeding_seconds BIGINT CHECK(min_seeding_seconds BETWEEN 1 AND 315360000),
 min_share_ratio DOUBLE PRECISION CHECK(min_share_ratio>0 AND min_share_ratio<=1000000),
 low_free_bytes BIGINT CHECK(low_free_bytes>0),
 delete_files BOOLEAN NOT NULL DEFAULT FALSE CHECK(NOT delete_files),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(min_seeding_seconds IS NOT NULL OR min_share_ratio IS NOT NULL OR low_free_bytes IS NOT NULL),
 CHECK(updated_at>=created_at)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_guard_seeding_configuration() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN
  RAISE EXCEPTION 'seeding configuration history cannot be removed';
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'seeding initial version must be one'; END IF;
 ELSE
  IF NEW.client_id IS DISTINCT FROM OLD.client_id
     OR NEW.created_at IS DISTINCT FROM OLD.created_at
     OR NEW.version<>OLD.version+1
     OR NEW.updated_at<OLD.updated_at THEN
   RAISE EXCEPTION 'seeding configuration identity or version changed';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER seeding_configuration_row_guard
 BEFORE INSERT OR UPDATE OR DELETE ON seeding_configuration
 FOR EACH ROW EXECUTE FUNCTION pulse_guard_seeding_configuration();
CREATE TRIGGER seeding_configuration_truncate_guard
 BEFORE TRUNCATE ON seeding_configuration
 FOR EACH STATEMENT EXECUTE FUNCTION pulse_guard_seeding_configuration();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM seeding_configuration) THEN
  RAISE EXCEPTION 'seeding configuration history prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE seeding_configuration;
DROP FUNCTION pulse_guard_seeding_configuration();
