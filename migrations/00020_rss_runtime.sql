-- +goose Up
DROP INDEX indexer_query_active_uniq;
CREATE UNIQUE INDEX indexer_query_active_uniq ON indexer_query(indexer_id,query_type,keyword,page,config_version) WHERE state='running';
ALTER TABLE wanted_item ADD COLUMN rss_seen_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch';
ALTER TABLE indexer ADD COLUMN rss_enabled BOOLEAN NOT NULL DEFAULT false,
 ADD COLUMN rss_kind TEXT NOT NULL DEFAULT 'torznab' CHECK(rss_kind IN ('torznab','rss')),
 ADD COLUMN rss_url_encrypted BYTEA,
 ADD COLUMN rss_interval_seconds INTEGER NOT NULL DEFAULT 300 CHECK(rss_interval_seconds BETWEEN 30 AND 86400),
 ADD COLUMN rss_next_poll_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 ADD COLUMN rss_last_query_id UUID REFERENCES indexer_query(id) ON DELETE SET NULL,
 ADD COLUMN rss_last_success_at TIMESTAMPTZ;
ALTER TABLE indexer_query ADD COLUMN rss_job_id UUID REFERENCES job(id) ON DELETE SET NULL;
CREATE INDEX rss_job_query ON indexer_query(rss_job_id) WHERE rss_job_id IS NOT NULL;
CREATE INDEX rss_due ON indexer(rss_next_poll_at) WHERE rss_enabled;
CREATE TABLE search_round_rss_query (
 round_id UUID NOT NULL REFERENCES search_round(id) ON DELETE CASCADE,
 query_id UUID NOT NULL REFERENCES indexer_query(id) ON DELETE CASCADE,
 PRIMARY KEY(round_id,query_id)
);

-- +goose Down
UPDATE job SET state='dead',lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,error='RSS runtime removed',finished_at=clock_timestamp() WHERE kind='rss_poll' AND state IN ('queued','running');
UPDATE indexer_query SET state='failed',error='RSS runtime removed',finished_at=clock_timestamp() WHERE query_type='rss' AND state='running';
DROP INDEX indexer_query_active_uniq;
CREATE UNIQUE INDEX indexer_query_active_uniq ON indexer_query(indexer_id,keyword,page,config_version) WHERE state='running';
ALTER TABLE wanted_item DROP COLUMN rss_seen_at;
DROP TABLE search_round_rss_query;
DROP INDEX rss_due;
DROP INDEX rss_job_query;
ALTER TABLE indexer_query DROP COLUMN rss_job_id;
ALTER TABLE indexer DROP COLUMN rss_last_success_at, DROP COLUMN rss_last_query_id,
 DROP COLUMN rss_next_poll_at, DROP COLUMN rss_interval_seconds, DROP COLUMN rss_url_encrypted,
 DROP COLUMN rss_kind, DROP COLUMN rss_enabled;
