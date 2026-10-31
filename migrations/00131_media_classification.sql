-- +goose Up
CREATE TABLE media_classification_policy (
 version BIGINT PRIMARY KEY CHECK(version>0),
 rules JSONB NOT NULL CHECK(jsonb_typeof(rules)='array' AND jsonb_array_length(rules)<=64),
 actor_id UUID NOT NULL REFERENCES app_user(id),
 request_key TEXT NOT NULL CHECK(length(request_key) BETWEEN 1 AND 160),
 request_hash TEXT NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 published_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,request_key)
);
CREATE TABLE media_classification_fact (
 media_id UUID PRIMARY KEY REFERENCES media(id),
 metadata_version BIGINT NOT NULL CHECK(metadata_version>0),
 facts JSONB NOT NULL CHECK(jsonb_typeof(facts)='object'),
 observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE subscription_classification_choice (
 subscription_id UUID PRIMARY KEY REFERENCES subscription(id),
 policy_version BIGINT NOT NULL REFERENCES media_classification_policy(version),
 root_id UUID NOT NULL REFERENCES library_root(id),
 root_generation BIGINT NOT NULL,
 result JSONB NOT NULL CHECK(result->>'status'='matched'),
 facts JSONB NOT NULL CHECK(jsonb_typeof(facts)='object'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
-- +goose StatementBegin
CREATE FUNCTION guard_media_classification_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'classification history is immutable'; END $$;
-- +goose StatementEnd
CREATE TRIGGER media_classification_policy_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON media_classification_policy FOR EACH STATEMENT EXECUTE FUNCTION guard_media_classification_history();
CREATE TRIGGER subscription_classification_choice_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON subscription_classification_choice FOR EACH STATEMENT EXECUTE FUNCTION guard_media_classification_history();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM media_classification_policy) OR EXISTS(SELECT 1 FROM media_classification_fact) OR EXISTS(SELECT 1 FROM subscription_classification_choice) THEN RAISE EXCEPTION 'classification history prevents downgrade'; END IF;
END $$;
-- +goose StatementEnd
DROP TABLE subscription_classification_choice;
DROP TABLE media_classification_fact;
DROP TABLE media_classification_policy;
DROP FUNCTION guard_media_classification_history();
