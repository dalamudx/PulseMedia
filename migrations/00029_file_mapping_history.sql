-- +goose Up
ALTER TABLE library_file ADD COLUMN mapping_revision BIGINT NOT NULL DEFAULT 1 CHECK(mapping_revision>0);
ALTER TABLE library_file_unit ADD COLUMN retired_at TIMESTAMPTZ;
-- Historical pairs remain because facts reference them; retiring never deletes a pair.
-- +goose StatementBegin
CREATE FUNCTION pulse_file_mapping_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE files UUID[]; object UUID;
BEGIN
 IF TG_OP='INSERT' THEN SELECT array_agg(DISTINCT file_id) INTO files FROM new_rows;
 ELSIF TG_OP='DELETE' THEN SELECT array_agg(DISTINCT file_id) INTO files FROM old_rows;
 ELSE
  SELECT array_agg(DISTINCT n.file_id) INTO files FROM new_rows n JOIN old_rows o USING(file_id,unit_id) WHERE n.retired_at IS DISTINCT FROM o.retired_at;
  UPDATE unit_fact uf SET revoked_at=clock_timestamp(),revocation_reason='file_mapping_corrected'
   FROM new_rows n JOIN old_rows o USING(file_id,unit_id)
   WHERE n.retired_at IS NOT NULL AND o.retired_at IS NULL AND uf.source_file_id=n.file_id AND uf.unit_id=n.unit_id AND uf.revoked_at IS NULL AND uf.stage IN ('imported','confirmed');
 END IF;
 FOR object IN SELECT DISTINCT x FROM unnest(files) x ORDER BY x LOOP
  UPDATE library_file SET mapping_revision=mapping_revision+1 WHERE id=object;
 END LOOP;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER file_mapping_revision_insert AFTER INSERT ON library_file_unit REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_file_mapping_revision();
CREATE TRIGGER file_mapping_revision_update AFTER UPDATE ON library_file_unit REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_file_mapping_revision();
CREATE TRIGGER file_mapping_revision_delete AFTER DELETE ON library_file_unit REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION pulse_file_mapping_revision();
-- +goose Down
-- Do not revive retired associations through a downgrade.
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM library_file_unit WHERE retired_at IS NOT NULL) THEN RAISE EXCEPTION 'cannot downgrade historical file mappings';END IF;END $$;
-- +goose StatementEnd
DROP TRIGGER file_mapping_revision_insert ON library_file_unit;
DROP TRIGGER file_mapping_revision_update ON library_file_unit;
DROP TRIGGER file_mapping_revision_delete ON library_file_unit;
DROP FUNCTION pulse_file_mapping_revision();
ALTER TABLE library_file_unit DROP COLUMN retired_at;
ALTER TABLE library_file DROP COLUMN mapping_revision;
