-- name: GetCookieCloudConfig :one
SELECT * FROM cookiecloud_configuration WHERE owner_id= @owner_id;

-- name: LockCookieCloudConfig :one
SELECT * FROM cookiecloud_configuration WHERE owner_id= @owner_id FOR UPDATE;

-- name: CreateCookieCloudConfig :one
INSERT INTO cookiecloud_configuration(owner_id,version,enabled,interval_seconds,server_origin,connection_encrypted)
VALUES(@owner_id,1,@enabled,@interval_seconds,@server_origin,@connection_encrypted)
RETURNING *;

-- name: UpdateCookieCloudConfig :one
UPDATE cookiecloud_configuration SET
 version=version+1, enabled= @enabled, interval_seconds= @interval_seconds,
 server_origin= @server_origin, connection_encrypted= @connection_encrypted,
 updated_at=clock_timestamp()
WHERE owner_id= @owner_id AND version= @expected_version
RETURNING *;
