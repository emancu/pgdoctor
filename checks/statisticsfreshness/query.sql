-- name: StatisticsFreshness :one
-- Returns statistics age for the current database.
-- Use to validate stats are meaningful before relying on usage-based checks.
--
-- A NULL stats_reset does not mean the counters are old. Only pg_stat_reset()
-- records a timestamp; a crash, unclean shutdown or rebuilt replica zeroes the
-- counters and leaves it NULL. All of those coincide with a server start, and a
-- clean restart preserves the counters (PG15+), so uptime is a lower bound on how
-- far back they reach when no reset was recorded.
SELECT
  stats_reset
  , extract(EPOCH FROM (now() - stats_reset))::bigint AS age_seconds
  , extract(EPOCH FROM (now() - pg_postmaster_start_time()))::bigint AS uptime_seconds
FROM pg_stat_database
WHERE datname = current_database();
