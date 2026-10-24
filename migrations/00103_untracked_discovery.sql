-- +goose Up
CREATE TABLE untracked_discovery_state (
 scope_id UUID PRIMARY KEY REFERENCES untracked_directory_scope(id) ON DELETE RESTRICT,
 requested_version BIGINT NOT NULL CHECK(requested_version > 0),
 requested_sequence BIGINT NOT NULL DEFAULT 0 CHECK(requested_sequence >= 0),
 completed_sequence BIGINT NOT NULL DEFAULT 0 CHECK(completed_sequence >= 0 AND completed_sequence <= requested_sequence),
 active_job_id UUID REFERENCES job(id) ON DELETE RESTRICT,
 active_sequence BIGINT NOT NULL DEFAULT 0 CHECK(active_sequence >= 0 AND active_sequence <= requested_sequence),
 scheduled_scope_version BIGINT NOT NULL DEFAULT 0 CHECK(scheduled_scope_version >= 0),
 next_sweep_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(active_job_id IS NULL OR (active_sequence > 0 AND scheduled_scope_version > 0))
);
CREATE TABLE untracked_discovered_file (
 id UUID PRIMARY KEY,
 scope_id UUID NOT NULL REFERENCES untracked_directory_scope(id) ON DELETE RESTRICT,
 scope_version BIGINT NOT NULL CHECK(scope_version > 0),
 relative_path TEXT NOT NULL CHECK(length(relative_path) BETWEEN 1 AND 4095 AND relative_path !~ '^/|/$|//|(^|/)\.\.?(/|$)' AND position(chr(92) in relative_path)=0),
 revision BIGINT NOT NULL DEFAULT 1 CHECK(revision > 0),
 device BIGINT NOT NULL CHECK(device >= 0),
 inode BIGINT NOT NULL CHECK(inode > 0),
 size BIGINT NOT NULL CHECK(size >= 0),
 mtime BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 first_observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 last_observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 observations BIGINT NOT NULL DEFAULT 1 CHECK(observations > 0),
 last_job_id UUID NOT NULL REFERENCES job(id) ON DELETE RESTRICT,
 last_token BIGINT NOT NULL CHECK(last_token > 0),
 UNIQUE(scope_id,relative_path),
 CHECK(last_observed_at >= first_observed_at),
 FOREIGN KEY(scope_id,scope_version) REFERENCES untracked_directory_scope_receipt(scope_id,version) ON DELETE RESTRICT
);
CREATE INDEX untracked_discovery_due ON untracked_discovery_state(next_sweep_at,scope_id);
CREATE INDEX untracked_discovered_page ON untracked_discovered_file(scope_id,created_at,id);
REVOKE ALL ON untracked_discovery_state,untracked_discovered_file FROM PUBLIC;

-- +goose Down
LOCK TABLE untracked_discovery_state,untracked_discovered_file,job IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM untracked_discovered_file)
  OR EXISTS(SELECT 1 FROM job WHERE kind='untracked_discovery' AND state IN ('queued','running')) THEN
  RAISE EXCEPTION 'cannot drop active discovery or observations';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE untracked_discovered_file;
DROP TABLE untracked_discovery_state;
