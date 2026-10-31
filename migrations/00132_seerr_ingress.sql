-- +goose Up
CREATE TABLE seerr_configuration (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton),
 source_id UUID NOT NULL UNIQUE,
 enabled BOOLEAN NOT NULL DEFAULT false,
 version BIGINT NOT NULL CHECK(version>0),
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 configured_by UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 credential_encrypted BYTEA NOT NULL CHECK(octet_length(credential_encrypted)>28)
);
CREATE TABLE seerr_ingress_receipt (
 source_id UUID NOT NULL REFERENCES seerr_configuration(source_id) ON DELETE RESTRICT,
 external_request_id TEXT NOT NULL CHECK(external_request_id ~ '^[1-9][0-9]{0,15}$'),
 payload_hash TEXT NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 request_id UUID NOT NULL UNIQUE REFERENCES media_request(id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(source_id,external_request_id)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_seerr_owner_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' OR ROW(OLD.source_id,OLD.owner_id) IS DISTINCT FROM ROW(NEW.source_id,NEW.owner_id) OR NEW.version<>OLD.version+1 THEN
  RAISE EXCEPTION 'seerr source ownership is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER seerr_owner_guard BEFORE UPDATE OR DELETE ON seerr_configuration FOR EACH ROW EXECUTE FUNCTION pulse_seerr_owner_guard();
CREATE FUNCTION pulse_seerr_receipt_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'seerr receipts are immutable' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM media_request WHERE id=NEW.request_id AND owner_id=NEW.owner_id AND state='pending' AND version=1) THEN
  RAISE EXCEPTION 'seerr ingress requires a pending family request' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER seerr_receipt_guard BEFORE INSERT OR UPDATE OR DELETE ON seerr_ingress_receipt FOR EACH ROW EXECUTE FUNCTION pulse_seerr_receipt_guard();
CREATE TRIGGER seerr_configuration_no_truncate BEFORE TRUNCATE ON seerr_configuration FOR EACH STATEMENT EXECUTE FUNCTION pulse_seerr_owner_guard();
CREATE TRIGGER seerr_receipt_no_truncate BEFORE TRUNCATE ON seerr_ingress_receipt FOR EACH STATEMENT EXECUTE FUNCTION pulse_seerr_receipt_guard();
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM seerr_configuration) THEN RAISE EXCEPTION 'seerr history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE seerr_ingress_receipt;
DROP TABLE seerr_configuration;
DROP FUNCTION pulse_seerr_receipt_guard();
DROP FUNCTION pulse_seerr_owner_guard();
