-- name: GetSiteAccountStatsConfig :one
SELECT * FROM site_account_stats_config WHERE indexer_id=$1 FOR UPDATE;

-- name: GetSiteAccountStatsConfigReceipt :one
SELECT * FROM site_account_stats_config_receipt WHERE owner_id=$1 AND indexer_id=$2 AND command_key=$3;

-- name: GetSiteAccountStatsConfigReceiptVersion :one
SELECT * FROM site_account_stats_config_receipt WHERE indexer_id=$1 AND version=$2;

-- name: CreateSiteAccountStatsConfig :one
INSERT INTO site_account_stats_config(indexer_id,expected_account_id,profile,enabled,interval_seconds,version,source_config_version)
VALUES($1,$2,$3,$4,$5,1,$6) RETURNING *;

-- name: UpdateSiteAccountStatsConfig :one
UPDATE site_account_stats_config SET expected_account_id=$2,profile=$3,enabled=$4,interval_seconds=$5,version=version+1,source_config_version=$6,updated_at=clock_timestamp() WHERE indexer_id=$1 RETURNING *;

-- name: CreateSiteAccountStatsConfigReceipt :exec
INSERT INTO site_account_stats_config_receipt(owner_id,indexer_id,command_key,version,request_hash,input,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7);
