-- name: LockAcquisitionCommandUser :one
SELECT id,is_active,role FROM app_user WHERE id=$1 FOR SHARE;
