-- name: StatisticsFreshness :one
-- Returns statistics age for the current database.
-- Use to validate stats are meaningful before relying on usage-based checks.
-- The age is computed here so it is measured against the server's clock and is not
-- truncated to whole days: a reset an hour ago is 3600, not 0.
SELECT
  stats_reset
  , extract(EPOCH FROM (now() - stats_reset))::bigint AS age_seconds
FROM pg_stat_database
WHERE datname = current_database();
