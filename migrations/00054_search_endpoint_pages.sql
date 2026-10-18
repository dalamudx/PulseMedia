-- +goose Up
CREATE TABLE search_round_endpoint_page (
 round_id UUID NOT NULL,
 indexer_id UUID NOT NULL,
 page INTEGER NOT NULL CHECK(page BETWEEN 0 AND 3),
 query_id UUID NOT NULL REFERENCES indexer_query_page(query_id),
 source_job_id UUID NOT NULL REFERENCES job(id),
 source_token BIGINT NOT NULL CHECK(source_token>0),
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(round_id,indexer_id,page),
 UNIQUE(round_id,indexer_id,query_id),
 FOREIGN KEY(round_id,indexer_id) REFERENCES search_round_endpoint(round_id,indexer_id)
);
-- +goose StatementBegin
CREATE FUNCTION guard_round_endpoint_page() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE q indexer_query%ROWTYPE; r search_round%ROWTYPE;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'round endpoint page history is immutable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM job j WHERE j.id=NEW.source_job_id AND j.kind='search_endpoint' AND j.state='running'
  AND j.lease_token=NEW.source_token AND j.lease_until>clock_timestamp()
  AND j.payload->>'round_id'=NEW.round_id::text AND j.payload->>'indexer_id'=NEW.indexer_id::text
  AND j.dedupe_key=NEW.round_id::text||':'||NEW.indexer_id::text)
 THEN RAISE EXCEPTION 'round endpoint page job source mismatch'; END IF;
 SELECT * INTO q FROM indexer_query WHERE id=NEW.query_id;
 SELECT * INTO r FROM search_round WHERE id=NEW.round_id;
 IF q.indexer_id IS DISTINCT FROM NEW.indexer_id OR q.page IS DISTINCT FROM NEW.page OR q.state<>'succeeded'
  OR q.finished_at IS NULL OR q.finished_at>r.deadline
  OR q.config_version IS DISTINCT FROM (r.decision_snapshot->'indexers'->>NEW.indexer_id::text)::BIGINT
  OR NEW.page<>(SELECT count(*) FROM search_round_endpoint_page WHERE round_id=NEW.round_id AND indexer_id=NEW.indexer_id)
 THEN RAISE EXCEPTION 'round endpoint page source mismatch'; END IF;
 IF EXISTS(SELECT 1 FROM search_round_endpoint_page p JOIN indexer_query_candidate previous_link ON previous_link.query_id=p.query_id JOIN indexer_query_candidate fresh ON fresh.candidate_id=previous_link.candidate_id WHERE p.round_id=NEW.round_id AND p.indexer_id=NEW.indexer_id AND fresh.query_id=NEW.query_id)
 THEN RAISE EXCEPTION 'round endpoint page overlaps existing source'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER round_endpoint_page_history BEFORE INSERT OR UPDATE OR DELETE ON search_round_endpoint_page FOR EACH ROW EXECUTE FUNCTION guard_round_endpoint_page();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM search_round_endpoint_page) THEN RAISE EXCEPTION 'round endpoint page history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER round_endpoint_page_history ON search_round_endpoint_page;
DROP FUNCTION guard_round_endpoint_page();
DROP TABLE search_round_endpoint_page;
