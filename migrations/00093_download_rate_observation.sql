-- +goose Up
-- Replaceable display telemetry; no business row, history or authority is changed.
CREATE TABLE download_rate_observation (
 task_id UUID PRIMARY KEY REFERENCES download_task(id) ON DELETE CASCADE,
 client_id UUID NOT NULL REFERENCES download_client(id) ON DELETE RESTRICT,
 task_stamp TEXT NOT NULL CHECK(task_stamp ~ '^[0-9a-f]{64}$'),
 task_updated_at TIMESTAMPTZ NOT NULL,
 client_stamp TEXT NOT NULL CHECK(client_stamp ~ '^[0-9a-f]{64}$'),
 source TEXT NOT NULL CHECK(source IN ('ordinary','automatic_tv','complete_tv','movie_upgrade')),
 download_rate_bytes_per_second BIGINT CHECK(download_rate_bytes_per_second >= 0),
 observed_progress DOUBLE PRECISION CHECK(observed_progress >= 0 AND observed_progress <= 1),
 observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- +goose Down
DROP TABLE download_rate_observation;
