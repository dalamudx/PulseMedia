-- +goose Up
CREATE VIEW qualified_scan_missing AS
SELECT c.file_id,c.root_id,c.first_run_id,c.last_run_id,file.rel_path,
 file.last_mutation_seq,file.mapping_revision,file.file_fingerprint,file.size_bytes,file.mtime_ns
FROM missing_candidate c JOIN library_file file ON file.id=c.file_id
JOIN scan_run first ON first.id=c.first_run_id JOIN scan_run latest ON latest.id=c.last_run_id
JOIN scan_finalization origin ON origin.scan_run_id=first.id
JOIN scan_finalization final ON final.scan_run_id=latest.id
JOIN library_root root ON root.id=c.root_id
WHERE c.state='open' AND file.retired_at IS NULL
 AND first.state='succeeded' AND latest.state='succeeded'
 AND origin.completed_at IS NOT NULL AND final.completed_at IS NOT NULL
 AND first.id<>latest.id AND latest.created_at>=first.finished_at+interval '5 minutes'
 AND NOT origin.missing_review_required AND NOT final.missing_review_required
 AND root.health='online' AND root.generation=latest.generation
 AND root.scan_token=latest.scan_token AND root.health_revision=final.completed_health_revision
 AND root.path=final.expected_path AND root.media_type=final.expected_media_type
 AND root.marker_filename=final.expected_marker_filename
 AND first.generation=latest.generation AND origin.expected_path=final.expected_path
 AND origin.expected_media_type=final.expected_media_type AND origin.expected_marker_filename=final.expected_marker_filename
 AND origin.root_device=final.root_device AND origin.root_inode=final.root_inode
 AND origin.completed_health_revision=final.expected_health_revision
 AND file.last_mutation_seq<=first.start_mutation_seq AND file.last_mutation_seq<=latest.start_mutation_seq
 AND NOT EXISTS(SELECT 1 FROM scan_observation o WHERE o.scan_run_id IN(first.id,latest.id) AND o.rel_path=file.rel_path);
CREATE TABLE scan_missing_receipt (
 file_id UUID NOT NULL, first_run_id UUID NOT NULL, last_run_id UUID NOT NULL,
 root_id UUID NOT NULL, decision TEXT NOT NULL CHECK(decision IN('present','missing')),
 rel_path TEXT NOT NULL, last_mutation_seq BIGINT NOT NULL, mapping_revision BIGINT NOT NULL,
 file_fingerprint TEXT,size_bytes BIGINT NOT NULL,mtime_ns BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(file_id,first_run_id),
 FOREIGN KEY(file_id,first_run_id) REFERENCES missing_candidate(file_id,first_run_id),
 FOREIGN KEY(file_id,root_id) REFERENCES library_file(id,root_id),
 FOREIGN KEY(last_run_id,root_id) REFERENCES scan_run(id,root_id)
);
-- +goose StatementBegin
CREATE FUNCTION forbid_scan_missing_receipt_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'scan missing receipts are immutable'; END $$;
-- +goose StatementEnd
CREATE TRIGGER scan_missing_receipt_immutable BEFORE UPDATE OR DELETE ON scan_missing_receipt
 FOR EACH ROW EXECUTE FUNCTION forbid_scan_missing_receipt_mutation();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM scan_missing_receipt) THEN
 RAISE EXCEPTION 'scan missing application receipts prevent downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TABLE scan_missing_receipt;
DROP FUNCTION forbid_scan_missing_receipt_mutation();
DROP VIEW qualified_scan_missing;
