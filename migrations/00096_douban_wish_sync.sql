-- +goose Up
CREATE TABLE douban_wish_sync_run (
 id UUID PRIMARY KEY,
 config_id UUID NOT NULL REFERENCES douban_sync_config(id),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 owner_id UUID NOT NULL REFERENCES app_user(id),
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','partial','review_required')),
 canonical_id TEXT,
 profile_number TEXT,
 incomplete BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 finished_at TIMESTAMPTZ,
 FOREIGN KEY(config_id,owner_id) REFERENCES douban_sync_config(id,owner_id),
 FOREIGN KEY(config_id,config_version) REFERENCES douban_sync_config_receipt(config_id,version),
 CHECK((status IN ('succeeded','partial')) = (finished_at IS NOT NULL))
);
CREATE UNIQUE INDEX douban_wish_sync_active ON douban_wish_sync_run(config_id) WHERE status IN ('queued','running','review_required');
CREATE TABLE douban_wish_sync_page (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 run_id UUID NOT NULL REFERENCES douban_wish_sync_run(id),
 media_type TEXT NOT NULL CHECK(media_type IN ('movie','tv')),
 page_start INT NOT NULL CHECK(page_start>=0 AND page_start<15000 AND page_start%15=0),
 execution_id UUID,
 execution_pid INT,
 execution_owner TEXT,
 execution_token BIGINT,
 page JSONB,
 finished BOOLEAN NOT NULL DEFAULT false,
 UNIQUE(run_id,media_type,page_start),
 UNIQUE(job_id,run_id),
 CHECK((execution_id IS NULL)=(execution_pid IS NULL)),
 CHECK((execution_id IS NULL)=(execution_owner IS NULL)),
 CHECK((execution_id IS NULL)=(execution_token IS NULL)),
 CHECK(page IS NULL OR execution_id IS NOT NULL),
 CHECK(NOT finished OR page IS NOT NULL)
);
CREATE TABLE douban_wish_sync_item (
 id UUID PRIMARY KEY,
 run_id UUID NOT NULL REFERENCES douban_wish_sync_run(id),
 page_job_id UUID NOT NULL REFERENCES douban_wish_sync_page(job_id),
 douban_id TEXT NOT NULL CHECK(douban_id COLLATE "C" ~ '^[1-9][0-9]{0,19}$'),
 media_type TEXT NOT NULL CHECK(media_type IN ('movie','tv')),
 source JSONB NOT NULL,
 source_hash TEXT NOT NULL CHECK(source_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 match JSONB,
 match_hash TEXT CHECK(match_hash COLLATE "C" ~ '^[0-9a-f]{64}$'),
 command_key TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','matched','created','unresolved','incomplete','ambiguous','conflict','unavailable')),
 subscription_id UUID REFERENCES subscription(id),
 receipt JSONB,
 UNIQUE(run_id,media_type,douban_id),
 FOREIGN KEY(page_job_id,run_id) REFERENCES douban_wish_sync_page(job_id,run_id),
 CHECK((match IS NULL)=(match_hash IS NULL)),
 CHECK((state='created')=(subscription_id IS NOT NULL AND receipt IS NOT NULL)),
 CHECK(state='created' OR (subscription_id IS NULL AND receipt IS NULL))
);
CREATE INDEX douban_wish_created_command ON douban_wish_sync_item(command_key) WHERE state='created';
-- +goose StatementBegin
CREATE FUNCTION pulse_douban_wish_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN RAISE EXCEPTION 'douban wish history protected'; END IF;
 IF TG_TABLE_NAME='douban_wish_sync_run' THEN
  IF (NEW.id,NEW.config_id,NEW.config_version,NEW.owner_id,NEW.created_at) IS DISTINCT FROM
     (OLD.id,OLD.config_id,OLD.config_version,OLD.owner_id,OLD.created_at)
   OR (OLD.canonical_id IS NOT NULL AND NEW.canonical_id IS DISTINCT FROM OLD.canonical_id)
   OR (OLD.profile_number IS NOT NULL AND NEW.profile_number IS DISTINCT FROM OLD.profile_number)
   OR (OLD.incomplete AND NOT NEW.incomplete)
   OR (OLD.status IN ('succeeded','partial','review_required') AND NEW IS DISTINCT FROM OLD)
  THEN RAISE EXCEPTION 'douban run immutable'; END IF;
 ELSIF TG_TABLE_NAME='douban_wish_sync_page' THEN
  IF (NEW.job_id,NEW.run_id,NEW.media_type,NEW.page_start) IS DISTINCT FROM (OLD.job_id,OLD.run_id,OLD.media_type,OLD.page_start)
   OR (OLD.execution_id IS NOT NULL AND (NEW.execution_id,NEW.execution_pid,NEW.execution_owner,NEW.execution_token) IS DISTINCT FROM (OLD.execution_id,OLD.execution_pid,OLD.execution_owner,OLD.execution_token))
   OR (OLD.page IS NOT NULL AND NEW.page IS DISTINCT FROM OLD.page)
   OR (OLD.finished AND NEW IS DISTINCT FROM OLD)
  THEN RAISE EXCEPTION 'douban page immutable'; END IF;
 ELSE
  IF (NEW.id,NEW.run_id,NEW.page_job_id,NEW.douban_id,NEW.media_type,NEW.source,NEW.source_hash,NEW.command_key) IS DISTINCT FROM
     (OLD.id,OLD.run_id,OLD.page_job_id,OLD.douban_id,OLD.media_type,OLD.source,OLD.source_hash,OLD.command_key)
   OR (OLD.match IS NOT NULL AND (NEW.match,NEW.match_hash) IS DISTINCT FROM (OLD.match,OLD.match_hash))
   OR (OLD.state NOT IN ('pending','matched') AND NEW IS DISTINCT FROM OLD)
   OR (OLD.state='matched' AND NEW.state='pending')
  THEN RAISE EXCEPTION 'douban item immutable'; END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER douban_wish_run_update BEFORE UPDATE ON douban_wish_sync_run FOR EACH ROW EXECUTE FUNCTION pulse_douban_wish_history();
CREATE TRIGGER douban_wish_page_update BEFORE UPDATE ON douban_wish_sync_page FOR EACH ROW EXECUTE FUNCTION pulse_douban_wish_history();
CREATE TRIGGER douban_wish_item_update BEFORE UPDATE ON douban_wish_sync_item FOR EACH ROW EXECUTE FUNCTION pulse_douban_wish_history();
CREATE TRIGGER douban_wish_run_history BEFORE DELETE OR TRUNCATE ON douban_wish_sync_run FOR EACH STATEMENT EXECUTE FUNCTION pulse_douban_wish_history();
CREATE TRIGGER douban_wish_page_history BEFORE DELETE OR TRUNCATE ON douban_wish_sync_page FOR EACH STATEMENT EXECUTE FUNCTION pulse_douban_wish_history();
CREATE TRIGGER douban_wish_item_history BEFORE DELETE OR TRUNCATE ON douban_wish_sync_item FOR EACH STATEMENT EXECUTE FUNCTION pulse_douban_wish_history();
-- The SQL key exactly matches doubanWishLockKey in the repository.
-- +goose StatementBegin
CREATE FUNCTION pulse_douban_wish_lock_key(id UUID) RETURNS BIGINT LANGUAGE SQL IMMUTABLE STRICT AS $$
 SELECT ('x'||substr(encode(sha256(convert_to('douban-wish:'||id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint & 9223372036854775807;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION pulse_douban_wish_permit_held(k BIGINT, backend INT) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid AND a.datid=l.database
 WHERE l.locktype='advisory' AND l.mode='ExclusiveLock' AND l.granted AND l.objsubid=1
 AND l.classid=(k>>32)::oid AND l.objid=(k&4294967295)::oid AND l.pid=backend
 AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND a.backend_type='client backend');
$$;
-- +goose StatementEnd
-- Deferred checks see the end clock, including time spent in audit/other triggers.
-- They run only for the transaction that writes this observation/result.
-- +goose StatementBegin
CREATE FUNCTION pulse_douban_wish_commit_fence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p douban_wish_sync_page; r douban_wish_sync_run; c douban_sync_config; j job; u app_user; snap JSONB; k BIGINT; root UUID;
BEGIN
 IF TG_TABLE_NAME='douban_wish_sync_item' THEN
  SELECT * INTO STRICT p FROM douban_wish_sync_page WHERE job_id=NEW.page_job_id;
  IF NEW.run_id<>p.run_id OR NEW.media_type<>p.media_type OR NEW.source->>'DoubanID'<>NEW.douban_id OR NEW.source->>'MediaType'<>NEW.media_type
   OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(p.page->'Items') v WHERE v=NEW.source)
  THEN RAISE EXCEPTION 'douban item binding invalid'; END IF;
 ELSE
  SELECT * INTO STRICT p FROM douban_wish_sync_page WHERE job_id=NEW.job_id;
  IF p.execution_id IS NULL THEN RETURN NULL; END IF;
 END IF;
 SELECT * INTO STRICT r FROM douban_wish_sync_run WHERE id=p.run_id;
 SELECT * INTO STRICT c FROM douban_sync_config WHERE id=r.config_id;
 SELECT * INTO STRICT j FROM job WHERE id=p.job_id;
 SELECT * INTO STRICT u FROM app_user WHERE id=r.owner_id;
 SELECT snapshot INTO STRICT snap FROM douban_sync_config_receipt WHERE config_id=r.config_id AND version=r.config_version;
 k:=pulse_douban_wish_lock_key(c.id);
 root:=CASE WHEN p.media_type='movie' THEN c.movie_root_id ELSE c.tv_root_id END;
 IF j.kind<>'douban_wish_sync' OR j.payload<>jsonb_build_object('version',1,'run_id',r.id,'page_type',p.media_type,'start',p.page_start)
  OR j.state NOT IN ('running','succeeded') OR j.lease_owner IS DISTINCT FROM p.execution_owner OR j.lease_token IS DISTINCT FROM p.execution_token
  OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp() OR p.execution_id IS NULL OR p.execution_pid IS NULL
  OR r.status NOT IN ('running','partial','succeeded') OR NOT c.enabled OR c.version<>r.config_version OR c.owner_id<>r.owner_id
  OR NOT u.is_active OR u.role NOT IN ('admin','member')
  OR c.douban_uid IS DISTINCT FROM snap->>'douban_uid'
  OR c.movie_root_id IS DISTINCT FROM (snap->>'movie_root_id')::uuid OR c.tv_root_id IS DISTINCT FROM (snap->>'tv_root_id')::uuid
  OR EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated)
  OR root IS NULL OR NOT EXISTS(SELECT 1 FROM library_root WHERE id=root AND media_type=p.media_type AND health='online')
  OR NOT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE l.locktype='advisory' AND l.granted AND l.objsubid=1
    AND l.pid=p.execution_pid AND l.classid=(k>>32)::oid AND l.objid=(k&4294967295)::oid
    AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND a.datname=current_database())
 THEN RAISE EXCEPTION 'douban wish execution fence expired'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER douban_wish_page_commit AFTER INSERT OR UPDATE ON douban_wish_sync_page DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_douban_wish_commit_fence();
CREATE CONSTRAINT TRIGGER douban_wish_item_commit AFTER INSERT OR UPDATE ON douban_wish_sync_item DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pulse_douban_wish_commit_fence();
REVOKE ALL ON douban_wish_sync_run,douban_wish_sync_page,douban_wish_sync_item FROM PUBLIC;

-- +goose Down
LOCK TABLE douban_wish_sync_run,douban_wish_sync_page,douban_wish_sync_item IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM douban_wish_sync_run) OR EXISTS(SELECT 1 FROM douban_wish_sync_page) OR EXISTS(SELECT 1 FROM douban_wish_sync_item) THEN
  RAISE EXCEPTION 'cannot drop douban wish history';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE douban_wish_sync_item,douban_wish_sync_page,douban_wish_sync_run;
DROP FUNCTION pulse_douban_wish_history(),pulse_douban_wish_commit_fence(),pulse_douban_wish_lock_key(UUID),pulse_douban_wish_permit_held(BIGINT,INT);
