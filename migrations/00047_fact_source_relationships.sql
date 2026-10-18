-- +goose Up
-- Preserve historical source evidence. NOT VALID keeps pre-existing rows intact,
-- while PostgreSQL enforces both relationships for new or changed source keys.
-- Deferral allows ingestion to record a fact and its mapping in either order
-- within the same transaction; an incomplete transaction must not commit.
ALTER TABLE unit_fact
    ADD CONSTRAINT unit_fact_source_task_fk
    FOREIGN KEY (source_task_id) REFERENCES download_task(id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED NOT VALID;

ALTER TABLE unit_fact
    ADD CONSTRAINT unit_fact_source_file_unit_fk
    FOREIGN KEY (source_file_id, unit_id)
    REFERENCES library_file_unit(file_id, unit_id)
    ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED NOT VALID;

CREATE INDEX unit_fact_source_task_idx ON unit_fact(source_task_id)
    WHERE source_task_id IS NOT NULL;

-- +goose Down
DROP INDEX unit_fact_source_task_idx;
ALTER TABLE unit_fact DROP CONSTRAINT unit_fact_source_file_unit_fk;
ALTER TABLE unit_fact DROP CONSTRAINT unit_fact_source_task_fk;
