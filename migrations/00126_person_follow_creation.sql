-- +goose Up
-- Creation commits independently while the original session keeps its physical lock.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION person_follow_write_valid(execution UUID, completed BOOLEAN) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
 SELECT EXISTS(SELECT 1 FROM person_follow_execution e
 JOIN person_follow_sync_page p ON p.job_id=e.job_id AND p.run_id=e.run_id
 JOIN person_follow_sync_run r ON r.id=e.run_id
 JOIN person_follow_config c ON c.id=r.config_id AND c.owner_id=r.owner_id AND c.version=r.config_version
 JOIN app_user u ON u.id=r.owner_id JOIN job j ON j.id=e.job_id
 JOIN policy pol ON pol.id=c.quality_policy_id AND pol.current_revision=c.quality_policy_revision
 WHERE e.id=execution AND e.released_at IS NULL
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
CREATE OR REPLACE FUNCTION guard_person_follow_source_commit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e person_follow_execution; r person_follow_sync_run; source person_follow_sync_source; p person_follow_sync_page;
BEGIN
 IF TG_TABLE_NAME='person_follow_sync_source' THEN
  SELECT * INTO STRICT e FROM person_follow_execution WHERE id=NEW.execution_id;
  SELECT * INTO STRICT r FROM person_follow_sync_run WHERE id=NEW.run_id;
  IF e.backend_pid<>pg_backend_pid() OR e.run_id<>r.id OR NOT person_follow_write_valid(e.id,false)
   OR (NEW.source->>'PersonID')::bigint<>(r.config_snapshot->'settings'->>'person_id')::bigint
   OR NEW.source->>'Role'<>r.config_snapshot->'settings'->>'role'
   OR (SELECT count(*) FROM person_follow_sync_item WHERE run_id=r.id)<>NEW.work_count
  THEN RAISE EXCEPTION 'person follow source commit rejected' USING ERRCODE='55000'; END IF;
 ELSIF TG_TABLE_NAME='person_follow_sync_item' THEN
  SELECT * INTO STRICT e FROM person_follow_execution WHERE id=NEW.writer_execution_id;
  SELECT * INTO STRICT source FROM person_follow_sync_source WHERE run_id=NEW.run_id;
  IF (NEW.state<>'created' AND e.backend_pid<>pg_backend_pid()) OR e.run_id<>NEW.run_id OR NOT person_follow_write_valid(e.id,false)
   OR NEW.source IS DISTINCT FROM (source.source->'Works'->NEW.ordinal)
   OR NEW.tmdb_id<>(NEW.source->>'TMDBID')::bigint OR NEW.media_type<>NEW.source->>'MediaType'
  THEN RAISE EXCEPTION 'person follow candidate commit rejected' USING ERRCODE='55000'; END IF;
  IF NEW.state='created' THEN
   SELECT * INTO STRICT r FROM person_follow_sync_run WHERE id=NEW.run_id;
   IF NOT EXISTS(SELECT 1 FROM subscription sub JOIN media_identity mi ON mi.media_id=sub.media_id
    WHERE sub.id=NEW.subscription_id AND sub.user_id=r.owner_id AND sub.root_id=NEW.root_id
    AND mi.namespace='tmdb.'||NEW.media_type AND mi.external_id=NEW.tmdb_id::text)
    OR NOT EXISTS(SELECT 1 FROM idempotency_record i WHERE i.actor_type='user' AND i.actor_id=r.owner_id::text
     AND i.idempotency_key='subscription.creation:'||NEW.command_key AND i.response_code=201
     AND i.response_body->>'action'='subscription.creation' AND i.response_body->'result'=NEW.receipt)
   THEN RAISE EXCEPTION 'person follow original creation result required' USING ERRCODE='55000'; END IF;
  END IF;
 ELSE
  SELECT * INTO STRICT e FROM person_follow_execution WHERE job_id=NEW.job_id AND released_at IS NULL;
  IF NOT person_follow_write_valid(e.id,true) THEN RAISE EXCEPTION 'person follow page completion rejected' USING ERRCODE='55000'; END IF;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose Down
LOCK TABLE person_follow_sync_item IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM person_follow_sync_item WHERE state='created') THEN RAISE EXCEPTION 'cannot discard person follow creation contract' USING ERRCODE='55000'; END IF; END $$;
CREATE OR REPLACE FUNCTION person_follow_write_valid(execution UUID, completed BOOLEAN) RETURNS BOOLEAN LANGUAGE SQL STABLE STRICT AS $$
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
CREATE OR REPLACE FUNCTION guard_person_follow_source_commit() RETURNS trigger LANGUAGE plpgsql AS $$
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
