-- +goose Up
-- Persist the completed traversal and bounded reconciliation position.
CREATE TABLE scan_finalization (
    scan_run_id UUID PRIMARY KEY REFERENCES scan_run(id) ON DELETE CASCADE,
    root_id UUID NOT NULL REFERENCES library_root(id),
    expected_path TEXT NOT NULL,
    expected_media_type TEXT NOT NULL,
    expected_marker_filename TEXT NOT NULL,
    expected_health_revision BIGINT NOT NULL,
    root_device BIGINT NOT NULL,
    root_inode BIGINT NOT NULL CHECK (root_inode > 0),
    cursor_file_id UUID,
    exhausted BOOLEAN NOT NULL DEFAULT false,
    completed_at TIMESTAMPTZ
);
CREATE INDEX scan_finalization_root_idx ON scan_finalization(root_id);


-- +goose StatementBegin
CREATE FUNCTION protect_sealed_scan_observation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE phase TEXT;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  SELECT state INTO phase FROM scan_run WHERE id=OLD.scan_run_id FOR UPDATE;
  IF phase IS NOT NULL AND phase <> 'running' THEN RAISE EXCEPTION 'sealed scan observations are immutable'; END IF;
 END IF;
 IF TG_OP <> 'DELETE' THEN
  SELECT state INTO phase FROM scan_run WHERE id=NEW.scan_run_id FOR UPDATE;
  IF phase IS NOT NULL AND phase <> 'running' THEN RAISE EXCEPTION 'sealed scan observations are immutable'; END IF;
  RETURN NEW;
 END IF;
 RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER protect_sealed_scan_observation BEFORE INSERT OR UPDATE OR DELETE ON scan_observation
FOR EACH ROW EXECUTE FUNCTION protect_sealed_scan_observation();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM scan_finalization f JOIN scan_run s ON s.id=f.scan_run_id
 JOIN library_root r ON r.id=f.root_id WHERE s.state='finalizing' AND s.scan_token=r.scan_token) THEN
  RAISE EXCEPTION 'active scan finalization prevents downgrade';
 END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER protect_sealed_scan_observation ON scan_observation;
DROP FUNCTION protect_sealed_scan_observation();
DROP TABLE scan_finalization;
