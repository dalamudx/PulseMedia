-- +goose Up
CREATE TABLE person_follow_sync_run (
 id UUID PRIMARY KEY CHECK(id <> '00000000-0000-0000-0000-000000000000'),
 config_id UUID NOT NULL,
 config_version BIGINT NOT NULL CHECK(config_version>0),
 owner_id UUID NOT NULL,
 config_snapshot JSONB NOT NULL CHECK(jsonb_typeof(config_snapshot)='object'),
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','partial','aborted','review_required')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 finished_at TIMESTAMPTZ,
 FOREIGN KEY(config_id,owner_id) REFERENCES person_follow_config(id,owner_id) ON DELETE RESTRICT,
 FOREIGN KEY(config_id,config_version) REFERENCES person_follow_config_history(config_id,version) ON DELETE RESTRICT,
 CHECK((status IN ('succeeded','partial','aborted'))=(finished_at IS NOT NULL)),
 CHECK(finished_at IS NULL OR finished_at>=created_at)
);
CREATE UNIQUE INDEX person_follow_one_active_run ON person_follow_sync_run(config_id) WHERE status IN ('queued','running','review_required');
CREATE INDEX person_follow_run_schedule ON person_follow_sync_run(config_id,config_version,finished_at DESC);
CREATE TABLE person_follow_sync_page (
 job_id UUID PRIMARY KEY REFERENCES job(id) ON DELETE RESTRICT,
 run_id UUID NOT NULL REFERENCES person_follow_sync_run(id) ON DELETE RESTRICT,
 page_start INTEGER NOT NULL CHECK(page_start>=0 AND page_start<10000 AND page_start%64=0),
 finished BOOLEAN NOT NULL DEFAULT false,
 UNIQUE(run_id,page_start),
 UNIQUE(job_id,run_id)
);
-- +goose StatementBegin
CREATE FUNCTION protect_person_follow_sync_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'person follow execution history is protected' USING ERRCODE='55000'; END IF;
 IF TG_TABLE_NAME='person_follow_sync_run' THEN
  IF ROW(NEW.id,NEW.config_id,NEW.config_version,NEW.owner_id,NEW.config_snapshot,NEW.created_at) IS DISTINCT FROM ROW(OLD.id,OLD.config_id,OLD.config_version,OLD.owner_id,OLD.config_snapshot,OLD.created_at)
   OR (OLD.status IN ('succeeded','partial','aborted','review_required') AND NEW IS DISTINCT FROM OLD)
   OR (OLD.status='running' AND NEW.status='queued') THEN
   RAISE EXCEPTION 'person follow run binding is immutable' USING ERRCODE='55000';
  END IF;
 ELSE
  IF ROW(NEW.job_id,NEW.run_id,NEW.page_start) IS DISTINCT FROM ROW(OLD.job_id,OLD.run_id,OLD.page_start) OR (OLD.finished AND NEW IS DISTINCT FROM OLD) THEN
   RAISE EXCEPTION 'person follow page binding is immutable' USING ERRCODE='55000';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION guard_person_follow_schedule_commit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c person_follow_config; u app_user; h person_follow_config_history; j job;
BEGIN
 IF EXISTS(SELECT 1 FROM restore_isolation WHERE singleton AND isolated) THEN RAISE EXCEPTION 'person follow scheduling unavailable' USING ERRCODE='55000'; END IF;
 SELECT * INTO STRICT c FROM person_follow_config WHERE id=NEW.config_id;
 SELECT * INTO STRICT u FROM app_user WHERE id=NEW.owner_id;
 SELECT * INTO STRICT h FROM person_follow_config_history WHERE config_id=NEW.config_id AND version=NEW.config_version;
 IF NOT c.enabled OR c.version<>NEW.config_version OR c.owner_id<>NEW.owner_id OR NOT u.is_active OR u.role NOT IN ('admin','member') OR NEW.config_snapshot<>h.snapshot
  OR NOT EXISTS(SELECT 1 FROM policy p WHERE p.id=c.quality_policy_id AND p.current_revision=c.quality_policy_revision)
  OR EXISTS(SELECT 1 FROM person_follow_indexer b JOIN indexer_lifecycle l ON l.indexer_id=b.indexer_id WHERE b.config_id=c.id AND (NOT l.enabled OR l.deleted_at IS NOT NULL))
  OR (c.movie_root_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM library_root WHERE id=c.movie_root_id AND media_type='movie' AND health='online'))
  OR (c.tv_root_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM library_root WHERE id=c.tv_root_id AND media_type='tv' AND health='online')) THEN
  RAISE EXCEPTION 'person follow scheduling authorization changed' USING ERRCODE='55000';
 END IF;
 SELECT j0.* INTO STRICT j FROM job j0 JOIN person_follow_sync_page p ON p.job_id=j0.id WHERE p.run_id=NEW.id AND p.page_start=0;
 IF j.kind<>'person_follow_sync' OR j.state<>'queued' OR j.payload<>jsonb_build_object('version',1,'run_id',NEW.id,'start',0) THEN
  RAISE EXCEPTION 'person follow initial dispatch mismatch' USING ERRCODE='55000';
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER person_follow_run_immutable BEFORE UPDATE ON person_follow_sync_run FOR EACH ROW EXECUTE FUNCTION protect_person_follow_sync_history();
CREATE TRIGGER person_follow_page_immutable BEFORE UPDATE ON person_follow_sync_page FOR EACH ROW EXECUTE FUNCTION protect_person_follow_sync_history();
CREATE TRIGGER person_follow_run_history BEFORE DELETE OR TRUNCATE ON person_follow_sync_run FOR EACH STATEMENT EXECUTE FUNCTION protect_person_follow_sync_history();
CREATE TRIGGER person_follow_page_history BEFORE DELETE OR TRUNCATE ON person_follow_sync_page FOR EACH STATEMENT EXECUTE FUNCTION protect_person_follow_sync_history();
CREATE CONSTRAINT TRIGGER person_follow_schedule_commit AFTER INSERT ON person_follow_sync_run DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_person_follow_schedule_commit();
REVOKE ALL ON person_follow_sync_run,person_follow_sync_page FROM PUBLIC;

-- +goose Down
LOCK TABLE person_follow_sync_run,person_follow_sync_page IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM person_follow_sync_run) OR EXISTS(SELECT 1 FROM person_follow_sync_page) THEN
  RAISE EXCEPTION 'cannot discard person follow execution history' USING ERRCODE='55000';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE person_follow_sync_page;
DROP TABLE person_follow_sync_run;
DROP FUNCTION guard_person_follow_schedule_commit();
DROP FUNCTION protect_person_follow_sync_history();
