-- +goose Up
ALTER TABLE indexer_query DROP CONSTRAINT indexer_query_query_type_check;
ALTER TABLE indexer_query ADD CONSTRAINT indexer_query_query_type_check CHECK(query_type IN ('search','rss','interactive','tv_episode'));
CREATE TABLE automatic_tv_round (
 id UUID PRIMARY KEY,
 wanted_id UUID NOT NULL REFERENCES wanted_item(id),
 subscription_id UUID NOT NULL REFERENCES subscription(id),
 owner_id UUID NOT NULL REFERENCES app_user(id),
 snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 snapshot_hash TEXT NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
 finalize_job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 created_at TIMESTAMPTZ NOT NULL,
 deadline TIMESTAMPTZ NOT NULL,
 state TEXT NOT NULL DEFAULT 'running' CHECK(state IN ('running','closed')),
 CHECK(deadline>created_at AND deadline<=created_at+interval '2 minutes')
);
CREATE UNIQUE INDEX automatic_tv_round_live ON automatic_tv_round(wanted_id) WHERE state='running';
CREATE TABLE automatic_tv_source (
 round_id UUID NOT NULL REFERENCES automatic_tv_round(id),
 indexer_id UUID NOT NULL REFERENCES indexer(id),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 parameters JSONB NOT NULL CHECK(jsonb_typeof(parameters)='object'),
 parameters_hash TEXT NOT NULL CHECK(parameters_hash ~ '^[0-9a-f]{64}$'),
 state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','succeeded','partial','failed','deferred','timeout')),
 PRIMARY KEY(round_id,indexer_id)
);
CREATE TABLE automatic_tv_query (
 round_id UUID NOT NULL,
 indexer_id UUID NOT NULL,
 page INTEGER NOT NULL CHECK(page BETWEEN 0 AND 3),
 query_id UUID NOT NULL UNIQUE REFERENCES indexer_query(id),
 source_job_id UUID NOT NULL REFERENCES job(id),
 source_token BIGINT NOT NULL CHECK(source_token>0),
 parameters JSONB NOT NULL CHECK(jsonb_typeof(parameters)='object'),
 parameters_hash TEXT NOT NULL CHECK(parameters_hash ~ '^[0-9a-f]{64}$'),
 PRIMARY KEY(round_id,indexer_id,page),
 FOREIGN KEY(round_id,indexer_id) REFERENCES automatic_tv_source(round_id,indexer_id)
);
CREATE TABLE automatic_tv_query_candidate (
 query_id UUID NOT NULL REFERENCES automatic_tv_query(query_id),
 candidate_id UUID NOT NULL REFERENCES release_candidate(id),
 source_stamp TEXT NOT NULL CHECK(source_stamp ~ '^[0-9a-f]{64}$'),
 title TEXT NOT NULL,
 guid TEXT NOT NULL,
 info_hash TEXT,
 link_encrypted BYTEA NOT NULL,
 quality_profile JSONB NOT NULL,
 size_bytes BIGINT NOT NULL,
 source_config_version BIGINT NOT NULL CHECK(source_config_version>0),
 PRIMARY KEY(query_id,candidate_id),
 FOREIGN KEY(query_id,candidate_id) REFERENCES indexer_query_candidate(query_id,candidate_id)
);
-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP IN ('DELETE','TRUNCATE') THEN RAISE EXCEPTION 'automatic TV history is durable'; END IF;
 IF TG_TABLE_NAME IN ('automatic_tv_query','automatic_tv_query_candidate') THEN RAISE EXCEPTION 'automatic TV query binding is immutable'; END IF;
 IF (to_jsonb(NEW)-'state') IS DISTINCT FROM (to_jsonb(OLD)-'state') THEN RAISE EXCEPTION 'automatic TV scope is immutable'; END IF;
 IF TG_TABLE_NAME='automatic_tv_round' THEN
  IF OLD.state='closed' AND NEW.state<>'closed' THEN RAISE EXCEPTION 'automatic TV round is closed'; END IF;
 ELSE
  IF OLD.state NOT IN ('queued','running') AND NEW.state IS DISTINCT FROM OLD.state THEN RAISE EXCEPTION 'automatic TV source is terminal'; END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_candidate_history BEFORE UPDATE OR DELETE ON automatic_tv_query_candidate FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_history();
CREATE TRIGGER automatic_tv_candidate_truncate BEFORE TRUNCATE ON automatic_tv_query_candidate FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_history();
CREATE TRIGGER automatic_tv_round_history BEFORE UPDATE OR DELETE ON automatic_tv_round FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_history();
CREATE TRIGGER automatic_tv_source_history BEFORE UPDATE OR DELETE ON automatic_tv_source FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_history();
CREATE TRIGGER automatic_tv_query_history BEFORE UPDATE OR DELETE ON automatic_tv_query FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_history();
CREATE TRIGGER automatic_tv_round_truncate BEFORE TRUNCATE ON automatic_tv_round FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_history();
CREATE TRIGGER automatic_tv_source_truncate BEFORE TRUNCATE ON automatic_tv_source FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_history();
CREATE TRIGGER automatic_tv_query_truncate BEFORE TRUNCATE ON automatic_tv_query FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_history();
-- Scope is sealed at Begin, including before a page proof is published.
-- +goose StatementBegin
CREATE FUNCTION guard_automatic_tv_query_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='TRUNCATE' THEN
  IF EXISTS(SELECT 1 FROM automatic_tv_query) THEN RAISE EXCEPTION 'automatic TV query history is durable'; END IF;
  RETURN NULL;
 END IF;
 IF EXISTS(SELECT 1 FROM automatic_tv_query WHERE query_id=OLD.id) THEN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'automatic TV query history is durable'; END IF;
  IF (to_jsonb(NEW)-'state'-'error'-'results_count'-'finished_at') IS DISTINCT FROM (to_jsonb(OLD)-'state'-'error'-'results_count'-'finished_at')
   OR OLD.state<>'running' THEN RAISE EXCEPTION 'automatic TV proven query is immutable'; END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER automatic_tv_indexer_scope BEFORE UPDATE OR DELETE ON indexer_query FOR EACH ROW EXECUTE FUNCTION guard_automatic_tv_query_scope();
CREATE TRIGGER automatic_tv_indexer_truncate BEFORE TRUNCATE ON indexer_query FOR EACH STATEMENT EXECUTE FUNCTION guard_automatic_tv_query_scope();
-- +goose StatementBegin
CREATE FUNCTION check_automatic_tv_query_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a automatic_tv_query; s automatic_tv_source; r automatic_tv_round; q indexer_query; target UUID;
BEGIN
 IF TG_TABLE_NAME='automatic_tv_query' THEN target:=NEW.query_id; ELSE target:=NEW.id; END IF;
 SELECT * INTO q FROM indexer_query WHERE id=target;
 SELECT * INTO a FROM automatic_tv_query WHERE query_id=target;
 IF q.query_type<>'tv_episode' AND NOT FOUND THEN RETURN NULL; END IF;
 IF a.query_id IS NULL OR q.query_type<>'tv_episode' THEN RAISE EXCEPTION 'TV query requires its original association'; END IF;
 SELECT * INTO s FROM automatic_tv_source WHERE round_id=a.round_id AND indexer_id=a.indexer_id;
 SELECT * INTO r FROM automatic_tv_round WHERE id=a.round_id;
 IF a.source_job_id<>s.job_id OR q.indexer_id<>s.indexer_id OR q.config_version<>s.config_version OR q.rss_job_id IS NOT NULL
  OR q.page<>a.page OR q.keyword<>'tv-episode-query-v1:'||a.parameters_hash
  OR (a.parameters-'offset') IS DISTINCT FROM (s.parameters-'offset')
  OR (a.parameters->>'offset')::integer<>a.page*50 OR (a.parameters->>'limit')::integer<>50
  OR (a.parameters->>'series_id')::bigint<>(r.snapshot->>'tmdb_id')::bigint
 THEN RAISE EXCEPTION 'automatic TV query association mismatch'; END IF;
 IF q.state='succeeded' AND (q.results_count<>(SELECT count(*) FROM automatic_tv_query_candidate WHERE query_id=q.id) OR q.finished_at>r.deadline OR NOT EXISTS(SELECT 1 FROM indexer_query_page p WHERE p.query_id=q.id AND p.page_offset=a.page*50 AND p.page_limit=50 AND p.item_count=q.results_count))
 THEN RAISE EXCEPTION 'automatic TV success requires page evidence'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER automatic_tv_query_binding AFTER INSERT ON automatic_tv_query DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_query_binding();
CREATE CONSTRAINT TRIGGER automatic_tv_query_proof AFTER INSERT OR UPDATE ON indexer_query DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_automatic_tv_query_binding();

-- +goose Down
LOCK TABLE automatic_tv_round,automatic_tv_source,automatic_tv_query,automatic_tv_query_candidate,job,indexer_query,audit_log,outbox_event IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM automatic_tv_round) OR EXISTS(SELECT 1 FROM automatic_tv_source) OR EXISTS(SELECT 1 FROM automatic_tv_query)
 OR EXISTS(SELECT 1 FROM automatic_tv_query_candidate)
 OR EXISTS(SELECT 1 FROM job WHERE kind IN ('automatic_tv_source','automatic_tv_finalize'))
 OR EXISTS(SELECT 1 FROM indexer_query WHERE query_type='tv_episode')
 OR EXISTS(SELECT 1 FROM audit_log WHERE action LIKE 'automatic_tv.%')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type LIKE 'automatic_tv.%')
 THEN RAISE EXCEPTION 'automatic TV durable history downgrade refused'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER automatic_tv_query_proof ON indexer_query;
DROP TRIGGER automatic_tv_indexer_scope ON indexer_query;
DROP TRIGGER automatic_tv_indexer_truncate ON indexer_query;
DROP TABLE automatic_tv_query_candidate,automatic_tv_query,automatic_tv_source,automatic_tv_round;
DROP FUNCTION check_automatic_tv_query_binding();
DROP FUNCTION guard_automatic_tv_query_scope();
DROP FUNCTION guard_automatic_tv_history();
ALTER TABLE indexer_query DROP CONSTRAINT indexer_query_query_type_check;
ALTER TABLE indexer_query ADD CONSTRAINT indexer_query_query_type_check CHECK(query_type IN ('search','rss','interactive'));
