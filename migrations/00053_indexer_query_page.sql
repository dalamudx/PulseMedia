-- +goose Up
CREATE TABLE indexer_query_page (
 query_id UUID PRIMARY KEY REFERENCES indexer_query(id),
 page_offset BIGINT NOT NULL CHECK(page_offset>=0),
 page_limit INTEGER NOT NULL CHECK(page_limit>0),
 item_count INTEGER NOT NULL CHECK(item_count>=0 AND item_count<=page_limit),
 offset_verified BOOLEAN NOT NULL,
 total BIGINT CHECK(total>=0),
 CHECK(total IS NULL OR offset_verified),
 CHECK(total IS NULL OR (item_count=0 AND page_offset>=total) OR (item_count>0 AND page_offset<total AND item_count<=total-page_offset))
);
-- +goose StatementBegin
CREATE FUNCTION guard_indexer_query_page_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'indexer page evidence is immutable';
END $$;
-- +goose StatementEnd
CREATE TRIGGER indexer_query_page_immutable BEFORE UPDATE OR DELETE ON indexer_query_page FOR EACH ROW EXECUTE FUNCTION guard_indexer_query_page_history();
-- +goose StatementBegin
CREATE FUNCTION check_indexer_query_page_commit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p indexer_query_page; q indexer_query; target UUID;
BEGIN
 IF TG_TABLE_NAME='indexer_query_page' THEN target:=NEW.query_id; ELSE target:=NEW.id; END IF;
 SELECT * INTO p FROM indexer_query_page WHERE query_id=target;
 IF NOT FOUND THEN RETURN NULL; END IF;
 SELECT * INTO q FROM indexer_query WHERE id=target;
 IF q.state<>'succeeded' OR q.page::bigint*50<>p.page_offset OR q.results_count<>p.item_count
  OR p.item_count<>(SELECT count(*) FROM indexer_query_candidate WHERE query_id=target) THEN
  RAISE EXCEPTION 'indexer page evidence and query must commit together';
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER indexer_query_page_commit AFTER INSERT ON indexer_query_page DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_indexer_query_page_commit();
CREATE CONSTRAINT TRIGGER indexer_query_with_page_commit AFTER UPDATE ON indexer_query DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_indexer_query_page_commit();
-- +goose StatementBegin
CREATE FUNCTION guard_proven_query_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.indexer_id,NEW.query_type,NEW.keyword,NEW.page,NEW.config_version,NEW.rss_job_id)
 IS DISTINCT FROM ROW(OLD.indexer_id,OLD.query_type,OLD.keyword,OLD.page,OLD.config_version,OLD.rss_job_id)
 AND EXISTS(SELECT 1 FROM indexer_query_page WHERE query_id=OLD.id) THEN
  RAISE EXCEPTION 'proven indexer query scope is immutable';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER indexer_query_proven_scope BEFORE UPDATE ON indexer_query FOR EACH ROW EXECUTE FUNCTION guard_proven_query_scope();
-- +goose StatementBegin
CREATE FUNCTION guard_proven_query_candidates() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' AND EXISTS(SELECT 1 FROM indexer_query_page WHERE query_id=OLD.query_id) THEN
  RAISE EXCEPTION 'proven query candidate links are immutable';
 END IF;
 IF TG_OP<>'DELETE' AND EXISTS(SELECT 1 FROM indexer_query_page WHERE query_id=NEW.query_id) THEN
  RAISE EXCEPTION 'proven query candidate links are immutable';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER indexer_query_proven_candidates BEFORE INSERT OR UPDATE OR DELETE ON indexer_query_candidate FOR EACH ROW EXECUTE FUNCTION guard_proven_query_candidates();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM indexer_query_page) THEN RAISE EXCEPTION 'indexer page history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER indexer_query_proven_scope ON indexer_query;
DROP TRIGGER indexer_query_proven_candidates ON indexer_query_candidate;
DROP FUNCTION guard_proven_query_scope();
DROP FUNCTION guard_proven_query_candidates();
DROP TRIGGER indexer_query_with_page_commit ON indexer_query;
DROP TABLE indexer_query_page;
DROP FUNCTION check_indexer_query_page_commit();
DROP FUNCTION guard_indexer_query_page_history();
