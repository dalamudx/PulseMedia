-- +goose Up
CREATE TABLE web_push_device (
 id UUID PRIMARY KEY,
 user_id UUID NOT NULL REFERENCES app_user(id),
 vapid_version TEXT NOT NULL CHECK (vapid_version ~ '^[0-9a-f]{64}$'),
 endpoint_digest TEXT NOT NULL UNIQUE CHECK (endpoint_digest ~ '^[0-9a-f]{64}$'),
 subscription_encrypted BYTEA NOT NULL CHECK (octet_length(subscription_encrypted) >= 40),
 status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked','expired')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK (updated_at >= created_at)
);
CREATE INDEX web_push_device_user_idx ON web_push_device (user_id, created_at DESC, id);
-- +goose StatementBegin
CREATE FUNCTION protect_web_push_device() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN
  RAISE EXCEPTION 'web push device history is immutable' USING ERRCODE = '55000';
 END IF;
 IF ROW(NEW.id,NEW.user_id,NEW.vapid_version,NEW.endpoint_digest,NEW.subscription_encrypted,NEW.created_at)
  IS DISTINCT FROM ROW(OLD.id,OLD.user_id,OLD.vapid_version,OLD.endpoint_digest,OLD.subscription_encrypted,OLD.created_at)
  OR OLD.status <> 'active' OR NEW.status NOT IN ('revoked','expired') OR NEW.updated_at <= OLD.updated_at THEN
  RAISE EXCEPTION 'web push device binding is immutable' USING ERRCODE = '55000';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER protect_web_push_device BEFORE UPDATE OR DELETE ON web_push_device FOR EACH ROW EXECUTE FUNCTION protect_web_push_device();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM web_push_device) THEN
  RAISE EXCEPTION 'cannot discard web push device history' USING ERRCODE = '55000';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE web_push_device;
DROP FUNCTION protect_web_push_device();
