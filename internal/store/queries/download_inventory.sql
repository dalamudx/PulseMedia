-- name: GetDownloadFileInventory :one
SELECT * FROM download_file_inventory WHERE task_id=$1;
-- name: CreateDownloadFileInventory :exec
INSERT INTO download_file_inventory(task_id,operation_id,client_id,info_hash,total_bytes,content_path,client_stamp,files) VALUES($1,$2,$3,$4,$5,$6,$7,$8);
