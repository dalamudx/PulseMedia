-- +goose Up
CREATE INDEX download_global_rate_retention ON download_global_rate_attempt(finished_at,job_id) WHERE state IN ('succeeded','failed');
CREATE INDEX release_candidate_retention ON release_candidate(created_at,id);

-- +goose Down
DROP INDEX release_candidate_retention;
DROP INDEX download_global_rate_retention;
