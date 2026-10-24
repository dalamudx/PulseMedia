-- +goose Up
CREATE TABLE person_follow_sync_source (
 run_id UUID PRIMARY KEY REFERENCES person_follow_sync_run(id) ON DELETE RESTRICT,
 execution_id UUID NOT NULL REFERENCES person_follow_execution(id) ON DELETE RESTRICT,
 source JSONB NOT NULL CHECK(jsonb_typeof(source)='object' AND jsonb_typeof(source->'Works')='array'),
 source_hash TEXT NOT NULL CHECK(source_hash ~ '^[0-9a-f]{64}$'),
 work_count INTEGER NOT NULL CHECK(work_count BETWEEN 0 AND 10000 AND work_count=jsonb_array_length(source->'Works')),
 observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE person_follow_sync_item (
 id UUID PRIMARY KEY,
 run_id UUID NOT NULL REFERENCES person_follow_sync_source(run_id) ON DELETE RESTRICT,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0 AND ordinal<10000),
 tmdb_id BIGINT NOT NULL CHECK(tmdb_id>0),
 media_type TEXT NOT NULL CHECK(media_type IN ('movie','tv')),
 source JSONB NOT NULL CHECK(jsonb_typeof(source)='object'),
 root_id UUID REFERENCES library_root(id) ON DELETE RESTRICT,
 command_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','created','excluded','review_required')),
 writer_execution_id UUID NOT NULL REFERENCES person_follow_execution(id) ON DELETE RESTRICT,
 subscription_id UUID REFERENCES subscription(id) ON DELETE RESTRICT,
 receipt JSONB,
 UNIQUE(run_id,ordinal),
 UNIQUE(run_id,media_type,tmdb_id),
 CHECK((state='created')=(subscription_id IS NOT NULL AND receipt IS NOT NULL)),
 CHECK(state='created' OR (subscription_id IS NULL AND receipt IS NULL)),
 CHECK(state NOT IN ('pending','created') OR (root_id IS NOT NULL AND length(command_key)>0))
);
CREATE INDEX person_follow_item_command ON person_follow_sync_item(command_key) WHERE state='created';
-- +goose StatementBegin
CREATE FUNCTION person_follow_write_valid(execution UUID, completed BOOLEAN) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM person_follow_execution e
 JOIN person_follow_sync_page p ON p.job_id=e.job_id AND p.run_id=e.run_id
 JOIN person_follow_sync_run r ON r.id=e.run_id
 JOIN person_follow_config c ON c.id=r.config_id AND c.owner_id=r.owner_id AND c.version=r.config_version
 JOIN app_user u ON u.id=r.owner_id JOIN job j ON j.id=e.job_id
 JOIN policy pol ON pol.id=c.quality_policy_id AND pol.current_revision=c.quality_policy_revision
 WHERE e.id=execution AND e.released_at IS NULL AND e.backend_pid=pg_backend_pid()
 AND pulse_person_follow_lock_held(pulse_person_follow_lock_key(c.id),e.backend_pid)
 AND c.enabled AND u.is_active AND u.role IN ('admin','member')
 AND NOT EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated)
 AND r.status IN ('running','succeeded','partial')
 AND j.kind='person_follow_sync' AND j.lease_owner=e.lease_owner AND j.lease_token=e.lease_token AND j.lease_until>clock_timestamp()
 AND ((NOT completed AND j.state='running' AND NOT p.finished) OR (completed AND j.state='succeeded' AND p.finished))
 AND j.payload=jsonb_build_object('version',1,'run_id',r.id,'start',p.page_start)
 AND (c.movie_root_id IS NULL OR EXISTS(SELECT 1 FROM library_root WHERE id=c.movie_root_id AND media_type='movie' AND health='online'))
 AND (c.tv_root_id IS NULL OR EXISTS(SELECT 1 FROM library_root WHERE id=c.tv_root_id AND media_type='tv' AND health='online'))
 AND NOT EXISTS(SELECT 1 FROM person_follow_indexer b JOIN indexer_lifecycle l ON l.indexer_id=b.indexer_id WHERE b.config_id=c.id AND (NOT l.enabled OR l.deleted_at IS NOT NULL)));
$$;
CREATE FUNCTION protect_person_follow_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'person follow source history is immutable' USING ERRCODE='55000'; END $$;
CREATE FUNCTION protect_person_follow_item() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' OR OLD.state<>'pending' OR NEW.state NOT IN ('created','review_required')
 OR ROW(NEW.id,NEW.run_id,NEW.ordinal,NEW.tmdb_id,NEW.media_type,NEW.source,NEW.root_id,NEW.command_key) IS DISTINCT FROM ROW(OLD.id,OLD.run_id,OLD.ordinal,OLD.tmdb_id,OLD.media_type,OLD.source,OLD.root_id,OLD.command_key)
 THEN RAISE EXCEPTION 'person follow candidate history is immutable' USING ERRCODE='55000'; END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION guard_person_follow_source_commit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e person_follow_execution; r person_follow_sync_run; source person_follow_sync_source; p person_follow_sync_page;
BEGIN
 IF TG_TABLE_NAME='person_follow_sync_source' THEN
  SELECT * INTO STRICT e FROM person_follow_execution WHERE id=NEW.execution_id;
  SELECT * INTO STRICT r FROM person_follow_sync_run WHERE id=NEW.run_id;
  IF e.run_id<>r.id OR NOT person_follow_write_valid(e.id,false)
   OR (NEW.source->>'PersonID')::bigint<>(r.config_snapshot->'settings'->>'person_id')::bigint
   OR NEW.source->>'Role'<>r.config_snapshot->'settings'->>'role'
   OR (SELECT count(*) FROM person_follow_sync_item WHERE run_id=r.id)<>NEW.work_count
  THEN RAISE EXCEPTION 'person follow source commit rejected' USING ERRCODE='55000'; END IF;
 ELSIF TG_TABLE_NAME='person_follow_sync_item' THEN
  SELECT * INTO STRICT e FROM person_follow_execution WHERE id=NEW.writer_execution_id;
  SELECT * INTO STRICT source FROM person_follow_sync_source WHERE run_id=NEW.run_id;
  IF e.run_id<>NEW.run_id OR NOT person_follow_write_valid(e.id,false)
   OR NEW.source IS DISTINCT FROM (source.source->'Works'->NEW.ordinal)
   OR NEW.tmdb_id<>(NEW.source->>'TMDBID')::bigint OR NEW.media_type<>NEW.source->>'MediaType'
  THEN RAISE EXCEPTION 'person follow candidate commit rejected' USING ERRCODE='55000'; END IF;
 ELSE
  SELECT * INTO STRICT e FROM person_follow_execution WHERE job_id=NEW.job_id AND released_at IS NULL;
  IF NOT person_follow_write_valid(e.id,true) THEN RAISE EXCEPTION 'person follow page completion rejected' USING ERRCODE='55000'; END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER person_follow_source_history BEFORE UPDATE OR DELETE OR TRUNCATE ON person_follow_sync_source FOR EACH STATEMENT EXECUTE FUNCTION protect_person_follow_source();
CREATE TRIGGER person_follow_item_identity BEFORE UPDATE ON person_follow_sync_item FOR EACH ROW EXECUTE FUNCTION protect_person_follow_item();
CREATE TRIGGER person_follow_item_history BEFORE DELETE OR TRUNCATE ON person_follow_sync_item FOR EACH STATEMENT EXECUTE FUNCTION protect_person_follow_source();
CREATE CONSTRAINT TRIGGER person_follow_source_commit AFTER INSERT ON person_follow_sync_source DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_person_follow_source_commit();
CREATE CONSTRAINT TRIGGER person_follow_item_commit AFTER INSERT OR UPDATE ON person_follow_sync_item DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_person_follow_source_commit();
CREATE CONSTRAINT TRIGGER person_follow_page_completion AFTER UPDATE ON person_follow_sync_page DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN(NEW.finished AND NOT OLD.finished) EXECUTE FUNCTION guard_person_follow_source_commit();
REVOKE ALL ON person_follow_sync_source,person_follow_sync_item FROM PUBLIC;

-- +goose Down
LOCK TABLE person_follow_sync_source,person_follow_sync_item IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM person_follow_sync_source) OR EXISTS(SELECT 1 FROM person_follow_sync_item) THEN RAISE EXCEPTION 'cannot discard person follow source history' USING ERRCODE='55000'; END IF; END $$;
-- +goose StatementEnd
DROP TRIGGER person_follow_page_completion ON person_follow_sync_page;
DROP TABLE person_follow_sync_item;
DROP TABLE person_follow_sync_source;
DROP FUNCTION guard_person_follow_source_commit();
DROP FUNCTION protect_person_follow_item();
DROP FUNCTION protect_person_follow_source();
DROP FUNCTION person_follow_write_valid(UUID,BOOLEAN);
