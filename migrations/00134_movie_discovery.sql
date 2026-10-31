-- +goose Up
CREATE TABLE subscription_movie_discovery_run (
    search_id UUID PRIMARY KEY REFERENCES interactive_search(id) ON DELETE RESTRICT,
    subscription_id UUID NOT NULL REFERENCES subscription(id) ON DELETE RESTRICT,
    input_stamp TEXT NOT NULL CHECK (length(input_stamp)=64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (search_id, subscription_id)
);
CREATE INDEX subscription_movie_discovery_run_subscription_idx ON subscription_movie_discovery_run(subscription_id,created_at);
CREATE TABLE subscription_movie_discovery (
    subscription_id UUID PRIMARY KEY REFERENCES subscription(id) ON DELETE RESTRICT,
    search_id UUID,
    next_discovery_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (search_id, subscription_id) REFERENCES subscription_movie_discovery_run(search_id, subscription_id) ON DELETE RESTRICT
);
-- +goose StatementBegin
CREATE FUNCTION pulse_movie_discovery_run_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'movie discovery history is immutable' USING ERRCODE='23514';
END $$;
-- +goose StatementEnd
CREATE TRIGGER movie_discovery_run_guard BEFORE UPDATE OR DELETE ON subscription_movie_discovery_run FOR EACH ROW EXECUTE FUNCTION pulse_movie_discovery_run_guard();
CREATE TRIGGER movie_discovery_run_truncate_guard BEFORE TRUNCATE ON subscription_movie_discovery_run FOR EACH STATEMENT EXECUTE FUNCTION pulse_movie_discovery_run_guard();
-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
 LOCK TABLE subscription_movie_discovery_run,subscription_movie_discovery IN ACCESS EXCLUSIVE MODE;
 IF EXISTS (SELECT 1 FROM subscription_movie_discovery_run) THEN
  RAISE EXCEPTION 'movie discovery history prevents downgrade' USING ERRCODE='23514';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE subscription_movie_discovery;
DROP TABLE subscription_movie_discovery_run;
DROP FUNCTION pulse_movie_discovery_run_guard();
