-- +goose Up
ALTER TABLE indexer_query DROP CONSTRAINT indexer_query_query_type_check;
ALTER TABLE indexer_query ADD CONSTRAINT indexer_query_query_type_check CHECK(query_type IN ('search','rss','interactive'));
DROP INDEX indexer_query_active_uniq;
CREATE UNIQUE INDEX indexer_query_active_uniq ON indexer_query(indexer_id,query_type,keyword,page,config_version) WHERE state='running' AND query_type<>'interactive';

CREATE TABLE interactive_search (
 id UUID PRIMARY KEY,
 actor_id UUID NOT NULL REFERENCES app_user(id),
 actor_role TEXT NOT NULL CHECK(actor_role IN ('admin','member')),
 query TEXT NOT NULL CHECK(length(query) BETWEEN 1 AND 512),
 media_id UUID REFERENCES media(id),
 subscription_id UUID REFERENCES subscription(id),
 ranking_policy JSONB NOT NULL CHECK(jsonb_typeof(ranking_policy)='object'),
 source_count INTEGER NOT NULL CHECK(source_count BETWEEN 1 AND 64),
 created_at TIMESTAMPTZ NOT NULL,
 deadline TIMESTAMPTZ NOT NULL,
 cancelled_at TIMESTAMPTZ,
 state TEXT NOT NULL DEFAULT 'running' CHECK(state IN ('running','succeeded','partial','failed','cancelled','timeout')),
 CHECK(deadline=created_at+interval '30 seconds')
);
CREATE INDEX interactive_search_due ON interactive_search(deadline,id) WHERE state='running';
CREATE TABLE interactive_search_source (
 search_id UUID NOT NULL REFERENCES interactive_search(id),
 indexer_id UUID NOT NULL REFERENCES indexer(id),
 config_version BIGINT NOT NULL CHECK(config_version>0),
 job_id UUID NOT NULL UNIQUE REFERENCES job(id),
 state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','succeeded','failed','deferred','timeout','cancelled')),
 reason TEXT NOT NULL DEFAULT '' CHECK(reason IN ('','succeeded','failed','deferred','timeout','cancelled','source_changed','lease_expired')),
 PRIMARY KEY(search_id,indexer_id)
);
CREATE TABLE interactive_search_query (
 search_id UUID NOT NULL,
 indexer_id UUID NOT NULL,
 page INTEGER NOT NULL CHECK(page BETWEEN 0 AND 3),
 query_id UUID NOT NULL UNIQUE REFERENCES indexer_query(id),
 source_job_id UUID NOT NULL REFERENCES job(id),
 source_token BIGINT NOT NULL CHECK(source_token>0),
 PRIMARY KEY(search_id,indexer_id,page),
 FOREIGN KEY(search_id,indexer_id) REFERENCES interactive_search_source(search_id,indexer_id)
);
-- +goose StatementBegin
CREATE FUNCTION guard_interactive_search_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'interactive search history is durable'; END IF;
 IF TG_TABLE_NAME='interactive_search_query' THEN RAISE EXCEPTION 'interactive query association is immutable'; END IF;
 IF TG_TABLE_NAME='interactive_search' THEN
  IF (to_jsonb(NEW)-'state'-'cancelled_at') IS DISTINCT FROM (to_jsonb(OLD)-'state'-'cancelled_at')
   OR (OLD.cancelled_at IS NOT NULL AND NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at)
  THEN RAISE EXCEPTION 'interactive search scope is immutable'; END IF;
 ELSE
  IF (to_jsonb(NEW)-'state'-'reason') IS DISTINCT FROM (to_jsonb(OLD)-'state'-'reason')
  THEN RAISE EXCEPTION 'interactive source scope is immutable'; END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER interactive_search_history BEFORE UPDATE OR DELETE ON interactive_search FOR EACH ROW EXECUTE FUNCTION guard_interactive_search_history();
CREATE TRIGGER interactive_source_history BEFORE UPDATE OR DELETE ON interactive_search_source FOR EACH ROW EXECUTE FUNCTION guard_interactive_search_history();
CREATE TRIGGER interactive_query_history BEFORE UPDATE OR DELETE ON interactive_search_query FOR EACH ROW EXECUTE FUNCTION guard_interactive_search_history();

-- +goose Down
LOCK TABLE interactive_search,interactive_search_source,interactive_search_query,job,indexer_query,audit_log,outbox_event,idempotency_record IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM interactive_search) OR EXISTS(SELECT 1 FROM interactive_search_source)
 OR EXISTS(SELECT 1 FROM interactive_search_query) OR EXISTS(SELECT 1 FROM job WHERE kind='interactive_search_source')
 OR EXISTS(SELECT 1 FROM indexer_query WHERE query_type='interactive')
 OR EXISTS(SELECT 1 FROM audit_log WHERE action LIKE 'interactive_search.%')
 OR EXISTS(SELECT 1 FROM outbox_event WHERE event_type LIKE 'interactive_search.%')
 OR EXISTS(SELECT 1 FROM idempotency_record WHERE idempotency_key LIKE 'interactive_search.%')
 THEN RAISE EXCEPTION 'interactive search durable history downgrade refused'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE interactive_search_query,interactive_search_source,interactive_search;
DROP FUNCTION guard_interactive_search_history();
DROP INDEX indexer_query_active_uniq;
CREATE UNIQUE INDEX indexer_query_active_uniq ON indexer_query(indexer_id,query_type,keyword,page,config_version) WHERE state='running';
ALTER TABLE indexer_query DROP CONSTRAINT indexer_query_query_type_check;
ALTER TABLE indexer_query ADD CONSTRAINT indexer_query_query_type_check CHECK(query_type IN ('search','rss'));
