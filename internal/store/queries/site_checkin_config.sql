-- name: GetSiteCheckinConfig :one
SELECT * FROM site_checkin_config WHERE indexer_id=$1 FOR UPDATE;

-- name: GetSiteCheckinConfigReceipt :one
SELECT * FROM site_checkin_config_receipt WHERE owner_id=$1 AND indexer_id=$2 AND command_key=$3;

-- name: GetSiteCheckinConfigReceiptVersion :one
SELECT * FROM site_checkin_config_receipt WHERE indexer_id=$1 AND version=$2;

-- name: CreateSiteCheckinConfig :one
INSERT INTO site_checkin_config(indexer_id,expected_account_id,profile,enabled,timezone,daily_at,version,source_config_version)
VALUES($1,$2,$3,$4,$5,$6,1,$7) RETURNING *;

-- name: UpdateSiteCheckinConfig :one
UPDATE site_checkin_config SET expected_account_id=$2,profile=$3,enabled=$4,timezone=$5,daily_at=$6,version=version+1,source_config_version=$7,updated_at=clock_timestamp() WHERE indexer_id=$1 RETURNING *;

-- name: CreateSiteCheckinConfigReceipt :exec
INSERT INTO site_checkin_config_receipt(owner_id,indexer_id,command_key,version,request_hash,input,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: SiteCheckinTimezoneKnown :one
SELECT pulse_checkin_timezone_known($1::text)::boolean AS known;
