-- +goose Up
ALTER TABLE scan_finalization
 ADD COLUMN indexed_files BIGINT NOT NULL DEFAULT 0 CHECK (indexed_files>=0),
 ADD COLUMN missing_review_required BOOLEAN NOT NULL DEFAULT false,
 ADD COLUMN completed_health_revision BIGINT;
ALTER TABLE library_file ADD CONSTRAINT library_file_id_root_unique UNIQUE(id,root_id);
ALTER TABLE scan_run ADD CONSTRAINT scan_run_id_root_unique UNIQUE(id,root_id);

-- Candidate rows are provisional until their source scan succeeds. They never
-- revoke files/facts. Each cycle retains its first source and terminal history.
CREATE TABLE missing_candidate (
 file_id UUID NOT NULL,
 root_id UUID NOT NULL REFERENCES library_root(id),
 first_run_id UUID NOT NULL,
 last_run_id UUID NOT NULL,
 state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','cleared')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 cleared_at TIMESTAMPTZ,
 clear_reason TEXT,
 PRIMARY KEY(file_id,first_run_id),
 FOREIGN KEY(file_id,root_id) REFERENCES library_file(id,root_id),
 FOREIGN KEY(first_run_id,root_id) REFERENCES scan_run(id,root_id),
 FOREIGN KEY(last_run_id,root_id) REFERENCES scan_run(id,root_id),
 CHECK((state='open' AND cleared_at IS NULL AND clear_reason IS NULL)
    OR (state='cleared' AND cleared_at IS NOT NULL AND clear_reason IS NOT NULL))
);
CREATE UNIQUE INDEX missing_candidate_active_file_idx ON missing_candidate(file_id) WHERE state='open';
CREATE INDEX missing_candidate_root_idx ON missing_candidate(root_id,last_run_id) WHERE state='open';

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM missing_candidate) OR EXISTS(
 SELECT 1 FROM scan_finalization f JOIN scan_run s ON s.id=f.scan_run_id
 JOIN library_root r ON r.id=f.root_id WHERE s.state='finalizing' AND s.scan_token=r.scan_token) THEN
 RAISE EXCEPTION 'scan missing evidence or active finalization prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE missing_candidate;
ALTER TABLE scan_run DROP CONSTRAINT scan_run_id_root_unique;
ALTER TABLE library_file DROP CONSTRAINT library_file_id_root_unique;
ALTER TABLE scan_finalization DROP COLUMN indexed_files,
 DROP COLUMN missing_review_required,DROP COLUMN completed_health_revision;
