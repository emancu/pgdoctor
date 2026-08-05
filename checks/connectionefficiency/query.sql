-- name: SessionStatistics :one
-- Gets session time statistics from pg_stat_database (PostgreSQL 14+).
-- These stats help analyze connection pool efficiency.
-- Returns zero values for PostgreSQL versions < 14 (columns don't exist).
SELECT
  COALESCE(SUM(idle_in_transaction_time), 0)::double precision AS total_idle_in_txn_time_ms
  , COALESCE(SUM(sessions), 0)::bigint AS total_sessions
  , COALESCE(SUM(sessions_abandoned), 0)::bigint AS sessions_abandoned
  , COALESCE(SUM(sessions_fatal), 0)::bigint AS sessions_fatal
  , COALESCE(SUM(sessions_killed), 0)::bigint AS sessions_killed
FROM pg_stat_database
WHERE
  datname IS NOT NULL
  AND datname NOT IN ('template0', 'template1');
