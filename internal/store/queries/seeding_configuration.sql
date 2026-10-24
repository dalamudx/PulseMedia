-- name: GetSeedingConfig :one
SELECT * FROM seeding_configuration WHERE client_id = @client_id;

-- name: LockSeedingConfig :one
SELECT * FROM seeding_configuration WHERE client_id = @client_id FOR UPDATE;

-- name: CreateSeedingConfig :one
INSERT INTO seeding_configuration(client_id,owner_id,version,client_updated_at,client_stamp,enabled,interval_seconds,match_mode,min_seeding_seconds,min_share_ratio,low_free_bytes,delete_files)
VALUES(@client_id,@owner_id,1,@client_updated_at,@client_stamp,@enabled,@interval_seconds,@match_mode,sqlc.narg('min_seeding_seconds')::bigint,sqlc.narg('min_share_ratio')::double precision,sqlc.narg('low_free_bytes')::bigint,FALSE)
RETURNING *;

-- name: UpdateSeedingConfig :one
UPDATE seeding_configuration SET
 owner_id = @owner_id, version = version+1,
 client_updated_at = @client_updated_at, client_stamp = @client_stamp,
 enabled = @enabled, interval_seconds = @interval_seconds, match_mode = @match_mode,
 min_seeding_seconds = sqlc.narg('min_seeding_seconds')::bigint,
 min_share_ratio = sqlc.narg('min_share_ratio')::double precision,
 low_free_bytes = sqlc.narg('low_free_bytes')::bigint,
 updated_at = clock_timestamp()
WHERE client_id = @client_id AND version = @expected_version
RETURNING *;
