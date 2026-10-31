-- name: ReadRuntimeQueueMetrics :many
-- Exclude retained successful history and never expose job kinds or payloads.
SELECT 'job'::text AS queue, state, count(*)::bigint AS count,
 COALESCE(max(EXTRACT(EPOCH FROM statement_timestamp()-not_before)) FILTER (WHERE state='queued' AND not_before<=statement_timestamp()),0)::double precision AS ready_age_seconds,
 count(*) FILTER (WHERE state='running' AND lease_until<=statement_timestamp())::bigint AS expired_leases
FROM job WHERE state IN ('queued','running','failed','dead') GROUP BY state
UNION ALL
SELECT 'outbox'::text AS queue,state,count(*)::bigint AS count,
 COALESCE(max(EXTRACT(EPOCH FROM statement_timestamp()-not_before)) FILTER (WHERE state='pending' AND not_before<=statement_timestamp()),0)::double precision AS ready_age_seconds,
 count(*) FILTER (WHERE state='delivering' AND lease_until<=statement_timestamp())::bigint AS expired_leases
FROM outbox_delivery WHERE state IN ('pending','delivering','failed','dead') GROUP BY state;

-- name: ReadRuntimeLockMetrics :one
-- Login-role scope works with least-privilege DML credentials, without pg_read_all_stats.
SELECT count(DISTINCT a.pid)::bigint AS waiters,
 COALESCE(max(GREATEST(0, EXTRACT(EPOCH FROM statement_timestamp()-l.waitstart))),0)::double precision AS oldest_wait_seconds
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_stat_activity a ON a.pid=l.pid
WHERE NOT l.granted AND a.datname=current_database()
 AND a.usename=session_user AND a.pid<>pg_backend_pid();

-- name: ReadRuntimeIndexerBudgetMetrics :one
-- Time gates only: health, permissions, leases and physical locks still govern execution.
SELECT count(*) FILTER (WHERE next_query_at<=statement_timestamp() AND (rate_limit_until IS NULL OR rate_limit_until<=statement_timestamp()))::bigint AS open,
 count(*) FILTER (WHERE next_query_at>statement_timestamp() AND (rate_limit_until IS NULL OR rate_limit_until<=statement_timestamp()))::bigint AS interval,
 count(*) FILTER (WHERE rate_limit_until>statement_timestamp())::bigint AS cooldown,
 COALESCE(max(EXTRACT(EPOCH FROM GREATEST(next_query_at,rate_limit_until,statement_timestamp())-statement_timestamp())),0)::double precision AS max_delay_seconds
FROM indexer;
