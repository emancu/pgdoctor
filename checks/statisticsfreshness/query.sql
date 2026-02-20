-- name: StatisticsFreshness :one
-- Returns statistics age for the current database.
-- Use to validate stats are meaningful before relying on usage-based checks.
SELECT
  stats_reset
  , coalesce(
    extract(EPOCH FROM (now() - stats_reset)) / 86400
    , 999
  )::int AS age_days
  , (now() - stats_reset) AS age_interval
FROM pg_stat_database
WHERE datname = current_database();
