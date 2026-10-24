-- name: GetBoundSubmissionClient :one
-- Inline the existing configuration fingerprint so old schema tests can still
-- read the client. Only schema112 advertises the new immutable binding contract.
SELECT sqlc.embed(c),
 CASE WHEN to_regclass('download_task_client_binding') IS NOT NULL
        OR to_regprocedure('pulse_assert_download_task_binding()') IS NOT NULL
 THEN encode(sha256(convert_to(jsonb_build_array(c.id,c.client_type,c.url,c.username,
        encode(c.password_encrypted,'hex'),COALESCE(c.remote_path_map,'{}'::jsonb))::text,'UTF8')),'hex')
 ELSE '' END::text AS config_stamp
FROM download_client c WHERE c.id=$1 FOR SHARE;

-- name: CreateDownloadTaskClientBinding :exec
INSERT INTO download_task_client_binding(task_id,client_id,client_stamp,job_id,lease_owner,lease_token)
VALUES($1,$2,$3,$4,$5,$6);

-- name: GetDownloadTaskClientBinding :one
SELECT * FROM download_task_client_binding WHERE task_id=$1;
