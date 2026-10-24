-- +goose Up
CREATE TABLE person_follow_execution (
 id UUID PRIMARY KEY CHECK(id <> '00000000-0000-0000-0000-000000000000'),
 job_id UUID NOT NULL,
 run_id UUID NOT NULL,
 lease_owner TEXT NOT NULL CHECK(lease_owner<>''),
 lease_token BIGINT NOT NULL CHECK(lease_token>0),
 backend_pid INTEGER NOT NULL CHECK(backend_pid>0),
 acquired_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 released_at TIMESTAMPTZ,
 FOREIGN KEY(job_id,run_id) REFERENCES person_follow_sync_page(job_id,run_id) ON DELETE RESTRICT,
 UNIQUE(job_id,lease_token),
 CHECK(released_at IS NULL OR released_at>=acquired_at)
);
CREATE UNIQUE INDEX person_follow_one_unreleased_execution ON person_follow_execution(job_id) WHERE released_at IS NULL;
-- +goose StatementBegin
CREATE FUNCTION pulse_person_follow_lock_key(id UUID) RETURNS BIGINT LANGUAGE SQL IMMUTABLE STRICT AS $$
 SELECT ('x'||substr(encode(sha256(convert_to('person-follow:'||id::text,'UTF8')),'hex'),1,16))::bit(64)::bigint & 9223372036854775807;
$$;
CREATE FUNCTION pulse_person_follow_lock_held(k BIGINT, backend INTEGER) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid AND a.datid=l.database
 WHERE l.locktype='advisory' AND l.mode='ExclusiveLock' AND l.granted AND l.objsubid=1
 AND l.classid=(k>>32)::oid AND l.objid=(k&4294967295)::oid AND l.pid=backend
 AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND a.backend_type='client backend');
$$;
CREATE FUNCTION protect_person_follow_execution() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE config UUID;
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'person follow execution history is protected' USING ERRCODE='55000'; END IF;
 IF OLD.released_at IS NOT NULL OR NEW.released_at IS NULL OR ROW(NEW.id,NEW.job_id,NEW.run_id,NEW.lease_owner,NEW.lease_token,NEW.backend_pid,NEW.acquired_at) IS DISTINCT FROM ROW(OLD.id,OLD.job_id,OLD.run_id,OLD.lease_owner,OLD.lease_token,OLD.backend_pid,OLD.acquired_at) THEN
  RAISE EXCEPTION 'person follow execution identity is immutable' USING ERRCODE='55000';
 END IF;
 SELECT config_id INTO STRICT config FROM person_follow_sync_run WHERE id=OLD.run_id;
 IF pg_backend_pid()<>OLD.backend_pid OR NOT pulse_person_follow_lock_held(pulse_person_follow_lock_key(config),OLD.backend_pid) THEN
  RAISE EXCEPTION 'person follow release requires the physical execution session' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION guard_person_follow_execution_commit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r person_follow_sync_run; p person_follow_sync_page; j job; c person_follow_config; u app_user;
BEGIN
 SELECT * INTO STRICT r FROM person_follow_sync_run WHERE id=NEW.run_id;
 SELECT * INTO STRICT p FROM person_follow_sync_page WHERE job_id=NEW.job_id;
 SELECT * INTO STRICT j FROM job WHERE id=NEW.job_id;
 SELECT * INTO STRICT c FROM person_follow_config WHERE id=r.config_id;
 SELECT * INTO STRICT u FROM app_user WHERE id=r.owner_id;
 IF pg_backend_pid()<>NEW.backend_pid OR NOT pulse_person_follow_lock_held(pulse_person_follow_lock_key(r.config_id),NEW.backend_pid)
 OR EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated)
 OR r.status NOT IN ('queued','running') OR p.finished OR p.run_id<>r.id
 OR NOT c.enabled OR c.version<>r.config_version OR c.owner_id<>r.owner_id OR NOT u.is_active OR u.role NOT IN ('admin','member')
 OR NOT EXISTS(SELECT 1 FROM policy WHERE id=c.quality_policy_id AND current_revision=c.quality_policy_revision)
 OR (c.movie_root_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM library_root WHERE id=c.movie_root_id AND media_type='movie' AND health='online'))
 OR (c.tv_root_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM library_root WHERE id=c.tv_root_id AND media_type='tv' AND health='online'))
 OR EXISTS(SELECT 1 FROM person_follow_indexer b JOIN indexer_lifecycle l ON l.indexer_id=b.indexer_id WHERE b.config_id=c.id AND (NOT l.enabled OR l.deleted_at IS NOT NULL))
 OR j.kind<>'person_follow_sync' OR j.state<>'running' OR j.lease_owner IS DISTINCT FROM NEW.lease_owner OR j.lease_token<>NEW.lease_token OR j.lease_until IS NULL OR j.lease_until<=clock_timestamp()
 OR j.payload<>jsonb_build_object('version',1,'run_id',r.id,'start',p.page_start)
 THEN RAISE EXCEPTION 'person follow execution authorization changed' USING ERRCODE='55000'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER person_follow_execution_identity BEFORE UPDATE ON person_follow_execution FOR EACH ROW EXECUTE FUNCTION protect_person_follow_execution();
CREATE TRIGGER person_follow_execution_history BEFORE DELETE OR TRUNCATE ON person_follow_execution FOR EACH STATEMENT EXECUTE FUNCTION protect_person_follow_execution();
CREATE CONSTRAINT TRIGGER person_follow_execution_commit AFTER INSERT ON person_follow_execution DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_person_follow_execution_commit();
REVOKE ALL ON person_follow_execution FROM PUBLIC;

-- +goose Down
LOCK TABLE person_follow_execution IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM person_follow_execution) THEN RAISE EXCEPTION 'cannot discard person follow execution history' USING ERRCODE='55000'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE person_follow_execution;
DROP FUNCTION guard_person_follow_execution_commit();
DROP FUNCTION protect_person_follow_execution();
DROP FUNCTION pulse_person_follow_lock_held(BIGINT,INTEGER);
DROP FUNCTION pulse_person_follow_lock_key(UUID);
