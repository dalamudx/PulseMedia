-- +goose Up
CREATE TABLE managed_sidecar_content (
 id uuid PRIMARY KEY,
 file_id uuid NOT NULL REFERENCES library_file(id),
 unit_id uuid NOT NULL REFERENCES media_unit(id),
 source_job_id uuid NOT NULL REFERENCES job(id),
 root_id uuid NOT NULL REFERENCES library_root(id),
 directory text NOT NULL,
 name text NOT NULL CHECK (name IN ('movie.nfo','tvshow.nfo','poster.jpg')),
 identity jsonb NOT NULL,
 content bytea NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 8388608),
 content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK (content_hash=encode(sha256(content),'hex'))
);
CREATE INDEX managed_sidecar_content_current ON managed_sidecar_content(file_id,unit_id,name,created_at DESC,id DESC);
CREATE TABLE managed_sidecar_update (
 id uuid PRIMARY KEY,
 owner_id uuid NOT NULL REFERENCES app_user(id),
 file_id uuid NOT NULL REFERENCES library_file(id),
 ticket jsonb NOT NULL,
 result jsonb NOT NULL,
 expires_at timestamptz NOT NULL,
 command_key text,
 permit uuid UNIQUE,
 lease_until timestamptz,
 released_at timestamptz,
 state text NOT NULL CHECK (state IN ('preview','held','executing','completed','review_required')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(owner_id,command_key),
 CHECK ((permit IS NULL)=(command_key IS NULL)),
 CHECK (permit IS NULL OR lease_until IS NOT NULL)
);
CREATE UNIQUE INDEX managed_sidecar_update_physical ON managed_sidecar_update(file_id) WHERE permit IS NOT NULL AND released_at IS NULL;
-- +goose StatementBegin
CREATE FUNCTION pulse_managed_sidecar_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='managed_sidecar_content' OR TG_OP IN ('DELETE','TRUNCATE') THEN
  RAISE EXCEPTION 'managed sidecar evidence is append only';
 END IF;
 IF OLD.ticket<>NEW.ticket OR OLD.owner_id<>NEW.owner_id OR OLD.file_id<>NEW.file_id OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at OR (OLD.permit IS NOT NULL AND (NEW.permit IS DISTINCT FROM OLD.permit OR NEW.command_key IS DISTINCT FROM OLD.command_key OR NEW.lease_until IS DISTINCT FROM OLD.lease_until)) OR (OLD.released_at IS NOT NULL AND NEW.released_at IS DISTINCT FROM OLD.released_at) THEN
  RAISE EXCEPTION 'managed sidecar authorization is immutable';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER managed_sidecar_content_immutable BEFORE UPDATE OR DELETE ON managed_sidecar_content FOR EACH ROW EXECUTE FUNCTION pulse_managed_sidecar_guard();
CREATE TRIGGER managed_sidecar_update_immutable BEFORE UPDATE OR DELETE ON managed_sidecar_update FOR EACH ROW EXECUTE FUNCTION pulse_managed_sidecar_guard();

CREATE TRIGGER managed_sidecar_content_no_truncate BEFORE TRUNCATE ON managed_sidecar_content FOR EACH STATEMENT EXECUTE FUNCTION pulse_managed_sidecar_guard();
CREATE TRIGGER managed_sidecar_update_no_truncate BEFORE TRUNCATE ON managed_sidecar_update FOR EACH STATEMENT EXECUTE FUNCTION pulse_managed_sidecar_guard();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM managed_sidecar_content) OR EXISTS(SELECT 1 FROM managed_sidecar_update) THEN
  RAISE EXCEPTION 'managed sidecar evidence requires retention';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE managed_sidecar_update;
DROP TABLE managed_sidecar_content;
DROP FUNCTION pulse_managed_sidecar_guard();
