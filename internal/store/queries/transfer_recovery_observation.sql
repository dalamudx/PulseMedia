-- name: IsTransferObservationFresh :one
WITH observed_clock AS MATERIALIZED (SELECT clock_timestamp() AS now)
SELECT sqlc.arg(observed_at)::timestamptz <= now
   AND sqlc.arg(observed_at)::timestamptz > now - interval '5 seconds' AS fresh
FROM observed_clock;
