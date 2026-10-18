-- name: GetEpisodeExecutionAuthority :one
SELECT * FROM episode_execution_authority WHERE operation_id=$1;
-- name: LockEpisodeSubmissionClient :one
SELECT * FROM download_client WHERE id=$1 FOR SHARE;
