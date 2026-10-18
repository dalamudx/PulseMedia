-- +goose Up
-- Bind inventory to its actual task scope; movie directory evidence has no episode authority.
ALTER TABLE download_task ADD CONSTRAINT download_task_inventory_identity UNIQUE(id,operation_id,client_id);
ALTER TABLE download_file_inventory DROP CONSTRAINT download_file_inventory_operation_id_fkey;
ALTER TABLE download_file_inventory ADD CONSTRAINT download_inventory_task_scope FOREIGN KEY(task_id,operation_id,client_id) REFERENCES download_task(id,operation_id,client_id);

-- +goose Down
-- +goose StatementBegin
DO $$BEGIN
 IF EXISTS(SELECT 1 FROM download_file_inventory i WHERE NOT EXISTS(SELECT 1 FROM episode_execution_authority e WHERE e.operation_id=i.operation_id)) THEN
 RAISE EXCEPTION 'movie inventory history cannot be discarded';
 END IF;
END$$;
-- +goose StatementEnd
ALTER TABLE download_file_inventory DROP CONSTRAINT download_inventory_task_scope;
ALTER TABLE download_file_inventory ADD CONSTRAINT download_file_inventory_operation_id_fkey FOREIGN KEY(operation_id) REFERENCES episode_execution_authority(operation_id);
ALTER TABLE download_task DROP CONSTRAINT download_task_inventory_identity;
