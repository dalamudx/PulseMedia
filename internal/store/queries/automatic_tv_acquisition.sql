-- name: AutomaticTVReadDecision :one
SELECT decision FROM automatic_tv_decision WHERE round_id=$1 AND finalize_job_id=$2 AND lease_owner=$3 AND lease_token=$4;

-- name: AutomaticTVFinalizationReady :one
SELECT r.deadline<=clock_timestamp() OR NOT EXISTS(SELECT 1 FROM automatic_tv_source s WHERE s.round_id=r.id AND s.state IN ('queued','running')) AS ready
FROM automatic_tv_round r WHERE r.id=$1;

-- name: AutomaticTVInsertAuthority :exec
INSERT INTO automatic_tv_execution_authority(operation_id,round_id,owner_id,subscription_id,unit_id,root_id,candidate_id,indexer_id,client_id,query_id,snapshot,decision_stamp)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12);

-- name: AutomaticTVInsertDecision :exec
INSERT INTO automatic_tv_decision(round_id,finalize_job_id,lease_owner,lease_token,operation_id,candidate_id,state,best_score,decision)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);
