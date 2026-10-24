-- +goose Up
CREATE TABLE untracked_directory_scope (
 id UUID PRIMARY KEY,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 enabled BOOLEAN NOT NULL,
 version BIGINT NOT NULL CHECK(version > 0),
 client_id UUID NOT NULL REFERENCES download_client(id) ON DELETE RESTRICT,
 target_root_id UUID NOT NULL REFERENCES library_root(id) ON DELETE RESTRICT,
 local_root TEXT NOT NULL CHECK(local_root ~ '^/[^/]' AND local_root !~ '/$|//|/\.\.?(/|$)' AND position(chr(92) in local_root)=0),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL CHECK(updated_at >= created_at)
);
CREATE TABLE untracked_directory_scope_receipt (
 scope_id UUID NOT NULL REFERENCES untracked_directory_scope(id) ON DELETE RESTRICT,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 command_key TEXT NOT NULL CHECK(command_key COLLATE "C" ~ '^[A-Za-z0-9:_.-]{1,160}$'),
 version BIGINT NOT NULL CHECK(version > 0),
 enabled BOOLEAN NOT NULL,
 local_root TEXT NOT NULL CHECK(local_root ~ '^/[^/]' AND local_root !~ '/$|//|/\.\.?(/|$)' AND position(chr(92) in local_root)=0),
 request_hash TEXT NOT NULL CHECK(request_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 input JSONB NOT NULL CHECK(jsonb_typeof(input)='object'),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 PRIMARY KEY(scope_id,command_key),
 UNIQUE(scope_id,version)
);
CREATE INDEX untracked_scope_history_source ON untracked_directory_scope_receipt(local_root) WHERE enabled;
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_scope_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'untracked scope history is protected'; END $$;
-- +goose StatementEnd
CREATE TRIGGER untracked_scope_no_delete BEFORE DELETE OR TRUNCATE ON untracked_directory_scope FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_scope_history_guard();
CREATE TRIGGER untracked_scope_receipt_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_directory_scope_receipt FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_scope_history_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_scope_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'untracked scope initial version'; END IF;
 ELSE
  IF NEW.id<>OLD.id OR NEW.owner_id<>OLD.owner_id OR NEW.created_at<>OLD.created_at OR NEW.version<>OLD.version+1 THEN
   RAISE EXCEPTION 'untracked scope version conflict';
  END IF;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM app_user WHERE id=NEW.owner_id AND role='admin' AND is_active) THEN
  RAISE EXCEPTION 'untracked scope requires current administrator';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER untracked_scope_version BEFORE INSERT OR UPDATE ON untracked_directory_scope FOR EACH ROW EXECUTE FUNCTION pulse_untracked_scope_version_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_scope_consistency() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c untracked_directory_scope; r untracked_directory_scope_receipt; sid uuid;
BEGIN
 IF TG_TABLE_NAME='untracked_directory_scope' THEN sid:=NEW.id; ELSE sid:=NEW.scope_id; END IF;
 SELECT * INTO STRICT c FROM untracked_directory_scope WHERE id=sid;
 SELECT * INTO STRICT r FROM untracked_directory_scope_receipt WHERE scope_id=sid AND version=c.version;
 IF r.owner_id<>c.owner_id OR r.enabled<>c.enabled OR r.local_root<>c.local_root OR r.snapshot<>c.snapshot
  OR (SELECT count(*) FROM untracked_directory_scope_receipt WHERE scope_id=sid)<>c.version
  OR c.version<>(SELECT max(version) FROM untracked_directory_scope_receipt WHERE scope_id=sid)
  OR (c.snapshot->>'id') IS DISTINCT FROM c.id::text
  OR (c.snapshot->>'owner_id') IS DISTINCT FROM c.owner_id::text
  OR (c.snapshot->>'enabled')::boolean IS DISTINCT FROM c.enabled
  OR (c.snapshot->>'version')::bigint IS DISTINCT FROM c.version
  OR (c.snapshot->>'client_id') IS DISTINCT FROM c.client_id::text
  OR (c.snapshot->>'target_root_id') IS DISTINCT FROM c.target_root_id::text
  OR (c.snapshot->>'local_root') IS DISTINCT FROM c.local_root
  OR (c.snapshot->>'created_at')::timestamptz IS DISTINCT FROM c.created_at
  OR (c.snapshot->>'updated_at')::timestamptz IS DISTINCT FROM c.updated_at THEN
  RAISE EXCEPTION 'untracked scope current/receipt mismatch';
 END IF;
 IF TG_TABLE_NAME='untracked_directory_scope_receipt' THEN
  r:=NEW;
  IF r.owner_id<>c.owner_id OR (r.snapshot->>'id') IS DISTINCT FROM sid::text
   OR (r.snapshot->>'owner_id') IS DISTINCT FROM r.owner_id::text
   OR (r.snapshot->>'version')::bigint IS DISTINCT FROM r.version
   OR (r.snapshot->>'enabled')::boolean IS DISTINCT FROM r.enabled
   OR (r.snapshot->>'local_root') IS DISTINCT FROM r.local_root
   OR (r.input->>'expected_version')::bigint IS DISTINCT FROM r.version-1
   OR (r.input->>'enabled')::boolean IS DISTINCT FROM r.enabled
   OR (r.input->>'client_id') IS DISTINCT FROM (r.snapshot->>'client_id')
   OR (r.input->>'target_root_id') IS DISTINCT FROM (r.snapshot->>'target_root_id')
   OR (r.input->>'remote_prefix') IS DISTINCT FROM (r.snapshot->>'remote_prefix')
   OR (r.input->>'expected_client_version')::timestamptz IS DISTINCT FROM (r.snapshot->>'client_version')::timestamptz
   OR (r.input->>'expected_root_generation')::bigint IS DISTINCT FROM (r.snapshot->>'target_root_generation')::bigint THEN
   RAISE EXCEPTION 'untracked scope input/receipt mismatch';
  END IF;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM app_user WHERE id=c.owner_id AND role='admin' AND is_active) THEN
  RAISE EXCEPTION 'untracked scope requires current administrator';
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER untracked_scope_consistency AFTER INSERT OR UPDATE ON untracked_directory_scope DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_scope_consistency();
CREATE CONSTRAINT TRIGGER untracked_scope_receipt_consistency AFTER INSERT ON untracked_directory_scope_receipt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_scope_consistency();
REVOKE ALL ON untracked_directory_scope,untracked_directory_scope_receipt FROM PUBLIC;

-- +goose Down
LOCK TABLE untracked_directory_scope,untracked_directory_scope_receipt IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM untracked_directory_scope) OR EXISTS(SELECT 1 FROM untracked_directory_scope_receipt) THEN
  RAISE EXCEPTION 'cannot drop untracked scope history';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE untracked_directory_scope_receipt;
DROP TABLE untracked_directory_scope;
DROP FUNCTION pulse_untracked_scope_consistency();
DROP FUNCTION pulse_untracked_scope_version_guard();
DROP FUNCTION pulse_untracked_scope_history_guard();
