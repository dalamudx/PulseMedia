-- +goose Up
CREATE TABLE untracked_review_request (
 id UUID PRIMARY KEY,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 scope_id UUID NOT NULL REFERENCES untracked_directory_scope(id) ON DELETE RESTRICT,
 file_id UUID NOT NULL REFERENCES untracked_discovered_file(id) ON DELETE RESTRICT,
 job_id UUID NOT NULL UNIQUE REFERENCES job(id) ON DELETE RESTRICT,
 command_key TEXT NOT NULL CHECK(command_key COLLATE "C" ~ '^[A-Za-z0-9:_.-]{1,160}$'),
 input_hash TEXT NOT NULL CHECK(input_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 request JSONB NOT NULL CHECK(jsonb_typeof(request)='object'),
 scope_snapshot JSONB NOT NULL CHECK(jsonb_typeof(scope_snapshot)='object'),
 file_snapshot JSONB NOT NULL CHECK(jsonb_typeof(file_snapshot)='object'),
 UNIQUE(scope_id,file_id,command_key)
);
CREATE TABLE untracked_review_request_receipt (
 request_id UUID PRIMARY KEY REFERENCES untracked_review_request(id) ON DELETE RESTRICT,
 receipt JSONB NOT NULL CHECK(jsonb_typeof(receipt)='object')
);
CREATE TABLE untracked_review_result (
 request_id UUID PRIMARY KEY REFERENCES untracked_review_request(id) ON DELETE RESTRICT,
 material_hash TEXT NOT NULL CHECK(material_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 review JSONB NOT NULL CHECK(jsonb_typeof(review)='object'),
 recorded_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE untracked_review_confirmation (
 id UUID PRIMARY KEY,
 owner_id UUID NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
 scope_id UUID NOT NULL REFERENCES untracked_directory_scope(id) ON DELETE RESTRICT,
 file_id UUID NOT NULL REFERENCES untracked_discovered_file(id) ON DELETE RESTRICT,
 review_id UUID NOT NULL REFERENCES untracked_review_result(request_id) ON DELETE RESTRICT,
 command_key TEXT NOT NULL CHECK(command_key COLLATE "C" ~ '^[A-Za-z0-9:_.-]{1,160}$'),
 input_hash TEXT NOT NULL CHECK(input_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 receipt JSONB NOT NULL CHECK(jsonb_typeof(receipt)='object'),
 UNIQUE(scope_id,file_id,command_key)
);
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_review_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'untracked review history is protected'; END $$;
-- +goose StatementEnd
CREATE TRIGGER untracked_review_request_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_review_request FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_review_history_guard();
CREATE TRIGGER untracked_review_receipt_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_review_request_receipt FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_review_history_guard();
CREATE TRIGGER untracked_review_result_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_review_result FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_review_history_guard();
CREATE TRIGGER untracked_review_confirmation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON untracked_review_confirmation FOR EACH STATEMENT EXECUTE FUNCTION pulse_untracked_review_history_guard();
-- +goose StatementBegin
CREATE FUNCTION pulse_untracked_review_consistency() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r untracked_review_request; c untracked_review_confirmation; v jsonb; rid uuid;
BEGIN
 IF TG_TABLE_NAME='untracked_review_request' THEN rid:=NEW.id;
 ELSIF TG_TABLE_NAME='untracked_review_confirmation' THEN rid:=NEW.review_id;
 ELSE rid:=NEW.request_id; END IF;
 SELECT * INTO STRICT r FROM untracked_review_request WHERE id=rid;
 SELECT receipt INTO STRICT v FROM untracked_review_request_receipt WHERE request_id=rid;
 IF v->'request' IS DISTINCT FROM r.request OR v->>'command_key' IS DISTINCT FROM r.command_key
  OR v->'execution_allowed' IS DISTINCT FROM 'false'::jsonb
  OR r.request->>'id' IS DISTINCT FROM r.id::text OR r.request->>'scope_id' IS DISTINCT FROM r.scope_id::text
  OR r.request->>'file_id' IS DISTINCT FROM r.file_id::text OR r.request->>'actor_id' IS DISTINCT FROM r.owner_id::text
  OR r.request->>'job_id' IS DISTINCT FROM r.job_id::text
  OR r.scope_snapshot->>'owner_id' IS DISTINCT FROM r.owner_id::text
  OR r.scope_snapshot->>'id' IS DISTINCT FROM r.scope_id::text
  OR r.file_snapshot->>'ID' IS DISTINCT FROM r.file_id::text
  OR r.file_snapshot->>'ScopeID' IS DISTINCT FROM r.scope_id::text
  OR NOT EXISTS(SELECT 1 FROM untracked_discovered_file f WHERE f.id=r.file_id AND f.scope_id=r.scope_id)
  OR NOT EXISTS(SELECT 1 FROM untracked_directory_scope s WHERE s.id=r.scope_id AND s.owner_id=r.owner_id)
  OR NOT EXISTS(SELECT 1 FROM job j WHERE j.id=r.job_id AND j.kind='untracked_review' AND j.max_attempts=3 AND j.payload=jsonb_build_object('request_id',r.id)) THEN
  RAISE EXCEPTION 'untracked review request mismatch';
 END IF;
 IF TG_TABLE_NAME='untracked_review_result' THEN
  IF NEW.review->'request' IS DISTINCT FROM r.request
   OR NEW.review->>'state' IS DISTINCT FROM (CASE WHEN NEW.review->'evidence'->'complete'='true'::jsonb THEN 'ready' ELSE 'review_required' END)
   OR NEW.review->>'material_hash' IS DISTINCT FROM NEW.material_hash
   OR (NEW.review->>'recorded_at')::timestamptz IS DISTINCT FROM NEW.recorded_at
   OR NEW.review->'execution_allowed' IS DISTINCT FROM 'false'::jsonb
   OR NEW.review->'stale' IS DISTINCT FROM 'false'::jsonb
   OR NOT EXISTS(SELECT 1 FROM job WHERE id=r.job_id AND state='succeeded') THEN
   RAISE EXCEPTION 'untracked review result mismatch';
  END IF;
 END IF;
 IF TG_TABLE_NAME='untracked_review_confirmation' THEN
  c:=NEW; v:=c.receipt;
  IF c.owner_id<>r.owner_id OR c.scope_id<>r.scope_id OR c.file_id<>r.file_id
   OR v->>'command_key' IS DISTINCT FROM c.command_key
   OR v->>'state' IS DISTINCT FROM 'confirmed_pending_organization'
   OR v->'execution_allowed' IS DISTINCT FROM 'false'::jsonb
   OR v->'material'->>'id' IS DISTINCT FROM c.id::text
   OR v->'material'->>'actor_id' IS DISTINCT FROM c.owner_id::text
   OR v->'material'->'review' IS DISTINCT FROM (SELECT review FROM untracked_review_result WHERE request_id=rid)
   OR v->'material'->'input'->>'review_id' IS DISTINCT FROM rid::text THEN
   RAISE EXCEPTION 'untracked review confirmation mismatch';
  END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER untracked_review_request_consistency AFTER INSERT ON untracked_review_request DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_review_consistency();
CREATE CONSTRAINT TRIGGER untracked_review_receipt_consistency AFTER INSERT ON untracked_review_request_receipt DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_review_consistency();
CREATE CONSTRAINT TRIGGER untracked_review_result_consistency AFTER INSERT ON untracked_review_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_review_consistency();
CREATE CONSTRAINT TRIGGER untracked_review_confirmation_consistency AFTER INSERT ON untracked_review_confirmation DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_untracked_review_consistency();
REVOKE ALL ON untracked_review_request,untracked_review_request_receipt,untracked_review_result,untracked_review_confirmation FROM PUBLIC;

-- +goose Down
LOCK TABLE untracked_review_request,untracked_review_request_receipt,untracked_review_result,untracked_review_confirmation,job IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM untracked_review_request) OR EXISTS(SELECT 1 FROM untracked_review_request_receipt)
  OR EXISTS(SELECT 1 FROM untracked_review_result) OR EXISTS(SELECT 1 FROM untracked_review_confirmation)
  OR EXISTS(SELECT 1 FROM job WHERE kind='untracked_review') THEN
  RAISE EXCEPTION 'cannot drop untracked review history';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE untracked_review_confirmation;
DROP TABLE untracked_review_result;
DROP TABLE untracked_review_request_receipt;
DROP TABLE untracked_review_request;
DROP FUNCTION pulse_untracked_review_consistency();
DROP FUNCTION pulse_untracked_review_history_guard();
