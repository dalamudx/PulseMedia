-- +goose Up
-- Configuration is owned by the current administrator. Only a safe origin is
-- public; URL prefix, user key and password live in one application-key envelope.
CREATE TABLE cookiecloud_configuration (
 owner_id UUID PRIMARY KEY REFERENCES app_user(id) ON DELETE RESTRICT,
 version BIGINT NOT NULL CHECK(version>0),
 enabled BOOLEAN NOT NULL,
 interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 60 AND 86400),
 server_origin TEXT NOT NULL CHECK(length(server_origin)<=2048 AND server_origin COLLATE "C" ~ '^https?://[^/?#@[:space:]]+$'),
 connection_encrypted BYTEA NOT NULL CHECK(octet_length(connection_encrypted) BETWEEN 1 AND 65536),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(updated_at>=created_at)
);

-- +goose StatementBegin
CREATE FUNCTION pulse_guard_cookiecloud_configuration() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN
  RAISE EXCEPTION 'CookieCloud configuration history cannot be removed';
 END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'CookieCloud initial version must be one'; END IF;
 ELSE
  IF NEW.owner_id IS DISTINCT FROM OLD.owner_id
     OR NEW.created_at IS DISTINCT FROM OLD.created_at
     OR NEW.version<>OLD.version+1
     OR NEW.updated_at<OLD.updated_at THEN
   RAISE EXCEPTION 'CookieCloud configuration version or owner changed';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER cookiecloud_configuration_row_guard
 BEFORE INSERT OR UPDATE OR DELETE ON cookiecloud_configuration
 FOR EACH ROW EXECUTE FUNCTION pulse_guard_cookiecloud_configuration();
CREATE TRIGGER cookiecloud_configuration_truncate_guard
 BEFORE TRUNCATE ON cookiecloud_configuration
 FOR EACH STATEMENT EXECUTE FUNCTION pulse_guard_cookiecloud_configuration();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
 IF EXISTS(SELECT 1 FROM cookiecloud_configuration) THEN
  RAISE EXCEPTION 'CookieCloud configuration history prevents downgrade';
 END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE cookiecloud_configuration;
DROP FUNCTION pulse_guard_cookiecloud_configuration();
