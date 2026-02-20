-- name: ConnectionStats :one
-- Gets overall connection statistics including pool sizing metrics.
SELECT
  current_setting('max_connections')::int AS max_connections
  , current_setting('superuser_reserved_connections')::int AS reserved_connections
  , count(*) AS total_connections
  , count(*) FILTER (WHERE state = 'active') AS active_connections
  , count(*) FILTER (WHERE state = 'idle') AS idle_connections
  , count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_transaction
  , count(*) FILTER (WHERE state = 'idle in transaction (aborted)') AS idle_in_transaction_aborted
  , count(*) FILTER (WHERE wait_event_type IS NOT NULL AND state = 'active') AS waiting_connections
FROM pg_stat_activity
WHERE pid != pg_backend_pid();

-- name: IdleInTransaction :many
-- Identifies connections stuck in 'idle in transaction' state.
-- Includes the timeout setting (in ms) for threshold calculation in Go.
SELECT
  pg_stat_activity.pid
  , pg_stat_activity.usename::text AS username
  , pg_stat_activity.datname::text AS database_name
  , pg_stat_activity.application_name::text AS application_name
  , pg_stat_activity.state::text AS state
  , extract(EPOCH FROM (now() - pg_stat_activity.xact_start))::bigint AS transaction_duration_seconds
  , left(pg_stat_activity.query, 200)::text AS query_preview
  , coalesce((
    SELECT pg_settings.setting::bigint
    FROM pg_settings
    WHERE pg_settings.name = 'idle_in_transaction_session_timeout'
  ), 0) AS timeout_ms
FROM pg_stat_activity
WHERE
  pg_stat_activity.state IN ('idle in transaction', 'idle in transaction (aborted)')
  AND pg_stat_activity.pid != pg_backend_pid()
ORDER BY pg_stat_activity.xact_start ASC;

-- name: LongIdleConnections :many
-- Identifies connections that have been idle for too long (potential pool leak).
SELECT
  pid
  , usename::text AS username
  , datname::text AS database_name
  , application_name::text AS application_name
  , client_addr::text AS client_address
  , state::text AS state
  , extract(EPOCH FROM (now() - state_change))::bigint AS idle_duration_seconds
  , extract(EPOCH FROM (now() - backend_start))::bigint AS connection_age_seconds
FROM pg_stat_activity
WHERE
  state = 'idle'
  AND pid != pg_backend_pid()
  AND (now() - state_change) > interval '30 minutes'
ORDER BY state_change ASC;
