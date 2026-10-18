-- +goose Up
-- Legacy rounds have no frozen target/endpoint or query evidence; do not replay them.
UPDATE search_round SET state='aborted',abort_reason='legacy snapshot missing',finished_at=clock_timestamp() WHERE state='running';
ALTER TABLE search_round ADD COLUMN decision_snapshot JSONB NOT NULL DEFAULT '{}';
ALTER TABLE search_round ADD COLUMN operation_id UUID;
CREATE UNIQUE INDEX search_round_active_wanted ON search_round(wanted_item_id) WHERE state='running';
ALTER TABLE indexer ADD COLUMN next_query_at TIMESTAMPTZ NOT NULL DEFAULT '-infinity';
CREATE TABLE indexer_query_candidate (
 query_id UUID NOT NULL REFERENCES indexer_query(id) ON DELETE CASCADE,
 candidate_id UUID NOT NULL REFERENCES release_candidate(id) ON DELETE CASCADE,
 PRIMARY KEY(query_id,candidate_id)
);
CREATE TABLE search_round_endpoint (
 round_id UUID NOT NULL REFERENCES search_round(id) ON DELETE CASCADE,
 indexer_id UUID NOT NULL REFERENCES indexer(id),
 query_id UUID REFERENCES indexer_query(id),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','returned','failed')),
 PRIMARY KEY(round_id,indexer_id)
);
-- +goose Down
DROP TABLE search_round_endpoint;
DROP TABLE indexer_query_candidate;
ALTER TABLE indexer DROP COLUMN next_query_at;
DROP INDEX search_round_active_wanted;
ALTER TABLE search_round DROP COLUMN operation_id;
ALTER TABLE search_round DROP COLUMN decision_snapshot;
