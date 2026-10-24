-- +goose Up
CREATE TABLE indexer_cookiecloud_credential (
 job_id UUID NOT NULL REFERENCES cookiecloud_sync_attempt(job_id),
 indexer_id UUID NOT NULL REFERENCES indexer(id),
 source_version BIGINT NOT NULL CHECK(source_version>0),
 credential_digest BYTEA NOT NULL CHECK(octet_length(credential_digest)=32),
 valid_until TIMESTAMPTZ,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(job_id,indexer_id)
);
CREATE INDEX indexer_cookiecloud_credential_latest ON indexer_cookiecloud_credential(indexer_id,source_version,recorded_at DESC,job_id DESC);
-- +goose StatementBegin
CREATE FUNCTION pulse_cookiecloud_credential_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'CookieCloud credential evidence immutable';
END $$;
-- +goose StatementEnd
CREATE TRIGGER indexer_cookiecloud_credential_history BEFORE UPDATE OR DELETE OR TRUNCATE ON indexer_cookiecloud_credential FOR EACH STATEMENT EXECUTE FUNCTION pulse_cookiecloud_credential_history_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_cookiecloud_credential_commit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a cookiecloud_sync_attempt; j job; i indexer; c cookiecloud_configuration;
BEGIN
 SELECT * INTO STRICT a FROM cookiecloud_sync_attempt WHERE job_id=NEW.job_id;
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id;
 SELECT * INTO STRICT i FROM indexer WHERE id=NEW.indexer_id;
 SELECT * INTO STRICT c FROM cookiecloud_configuration WHERE owner_id=a.owner_id;
 IF a.state<>'succeeded' OR j.kind<>'cookiecloud_sync' OR j.state<>'succeeded'
 OR j.lease_owner IS DISTINCT FROM a.execution_owner OR j.lease_token IS DISTINCT FROM a.execution_token
 OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp() OR a.execution_pid IS DISTINCT FROM pg_backend_pid()
 OR NOT c.enabled OR c.version<>a.config_version
 OR NOT EXISTS(SELECT 1 FROM app_user WHERE id=a.owner_id AND is_active AND role='admin')
 OR NOT pulse_cookiecloud_sync_gate_held(a.owner_id) OR NOT pulse_stats_gate_held(i.id)
 OR i.config_version<>NEW.source_version OR sha256(i.api_key_encrypted)<>NEW.credential_digest
 OR (NEW.valid_until IS NOT NULL AND NEW.valid_until<=clock_timestamp())
 OR pulse_cookiecloud_sync_binding(a.owner_id) IS DISTINCT FROM current_setting('pulse.cookiecloud_binding',true)
 THEN RAISE EXCEPTION 'CookieCloud credential evidence fence expired'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER indexer_cookiecloud_credential_commit AFTER INSERT ON indexer_cookiecloud_credential DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_cookiecloud_credential_commit_guard();
REVOKE ALL ON indexer_cookiecloud_credential FROM PUBLIC;

-- +goose Down
LOCK TABLE indexer_cookiecloud_credential IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM indexer_cookiecloud_credential) THEN RAISE EXCEPTION 'cannot drop CookieCloud credential evidence'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE indexer_cookiecloud_credential;
DROP FUNCTION pulse_cookiecloud_credential_history_guard(),pulse_cookiecloud_credential_commit_guard();
