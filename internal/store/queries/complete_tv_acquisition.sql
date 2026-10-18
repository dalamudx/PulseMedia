-- name: CreateCompleteTVExecutionAuthority :exec
INSERT INTO complete_tv_execution_authority(operation_id,review_id,subscription_id,candidate_id,root_id,client_id,actor_id,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: GetCompleteTVExecutionAuthority :one
SELECT * FROM complete_tv_execution_authority WHERE operation_id=$1;

