-- name: ReadSelectedMovieDownloaderSnapshot :one
SELECT id,encode(sha256(convert_to(jsonb_build_object('type',client_type,'url',url,'user',username,'cipher',encode(password_encrypted,'hex'),'map',remote_path_map)::text,'UTF8')),'hex') AS config_digest FROM download_client WHERE id=$1;

-- name: LockSelectedMovieDownloader :exec
SELECT id FROM download_client WHERE id=$1 FOR NO KEY UPDATE;

-- name: LockSelectedMovieIdentity :exec
SELECT pg_advisory_xact_lock(hashtextextended('tmdb.movie:' || sqlc.arg(tmdb_id)::text,0));

-- name: HasSelectedMovieCandidateObservation :one
SELECT EXISTS(SELECT 1 FROM indexer_query q JOIN indexer_query_candidate qc ON qc.query_id=q.id WHERE q.indexer_id=sqlc.arg(indexer_id) AND q.config_version=sqlc.arg(config_version) AND q.state='succeeded' AND q.finished_at>clock_timestamp()-interval '30 minutes' AND qc.candidate_id=sqlc.arg(candidate_id))::boolean;
