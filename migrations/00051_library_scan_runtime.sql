-- +goose Up
CREATE TABLE library_scan_schedule (
 root_id UUID PRIMARY KEY REFERENCES library_root(id),
 next_run_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 last_scan_job_id UUID REFERENCES job(id)
);
CREATE TABLE library_scan_job (
 job_id UUID PRIMARY KEY REFERENCES job(id),
 root_id UUID NOT NULL REFERENCES library_root(id),
 phase TEXT NOT NULL CHECK(phase IN('scan','missing')),
 run_id UUID,
 FOREIGN KEY(run_id,root_id) REFERENCES scan_run(id,root_id)
);
CREATE UNIQUE INDEX job_library_root_lane ON job ((payload->>'root_id'))
 WHERE kind IN('library_scan','library_missing') AND state IN('queued','running');
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM library_scan_job l JOIN job j ON j.id=l.job_id WHERE j.state IN('queued','running')) THEN
 RAISE EXCEPTION 'active library scan work prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP INDEX job_library_root_lane;
DROP TABLE library_scan_job;
DROP TABLE library_scan_schedule;
