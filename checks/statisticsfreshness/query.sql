-- name: StatisticsFreshness :one
-- Returns statistics age for the current database.
-- Only pg_stat_reset() records a timestamp; a crash or rebuilt replica zeroes the
-- counters silently, so uptime is the lower bound when stats_reset is NULL.
SELECT
  stats_reset
  , extract(EPOCH FROM (now() - stats_reset))::bigint AS age_seconds
  , extract(EPOCH FROM (now() - pg_postmaster_start_time()))::bigint AS uptime_seconds
FROM pg_stat_database
WHERE datname = current_database();
