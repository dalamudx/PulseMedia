-- name: ReadBoundSeedingInspectionClient :one
-- Match the original send fingerprint without taking a write lock for this transient read.
SELECT sqlc.embed(c),
 CASE WHEN to_regclass('download_task_client_binding') IS NOT NULL
        OR to_regprocedure('pulse_assert_download_task_binding()') IS NOT NULL
 THEN encode(sha256(convert_to(jsonb_build_array(c.id,c.client_type,c.url,c.username,
        encode(c.password_encrypted,'hex'),COALESCE(c.remote_path_map,'{}'::jsonb))::text,'UTF8')),'hex')
 ELSE '' END::text AS config_stamp
FROM download_client c WHERE c.id=$1;
