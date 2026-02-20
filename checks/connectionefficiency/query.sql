-- name: SessionStatistics :one
-- Gets session time statistics from pg_stat_database (PostgreSQL 14+).
-- These stats help analyze connection pool efficiency.
-- Returns zero values for PostgreSQL versions < 14 (columns don't exist).
SELECT
  COALESCE(SUM(session_time), 0)::double precision AS total_session_time_ms
  , COALESCE(SUM(active_time), 0)::double precision AS total_active_time_ms
  , COALESCE(SUM(idle_in_transaction_time), 0)::double precision AS total_idle_in_txn_time_ms
  , COALESCE(SUM(sessions), 0)::bigint AS total_sessions
  , COALESCE(SUM(sessions_abandoned), 0)::bigint AS sessions_abandoned
  , COALESCE(SUM(sessions_fatal), 0)::bigint AS sessions_fatal
  , COALESCE(SUM(sessions_killed), 0)::bigint AS sessions_killed
  -- Calculate session busy ratio (active_time / session_time)
  , CASE
    WHEN COALESCE(SUM(session_time), 0) > 0
      THEN ROUND((COALESCE(SUM(active_time), 0) / COALESCE(SUM(session_time), 0) * 100)::numeric, 2)
    ELSE 0
  END::double precision AS session_busy_ratio_percent
FROM pg_stat_database
WHERE
  datname IS NOT NULL
  AND datname NOT IN ('template0', 'template1');
