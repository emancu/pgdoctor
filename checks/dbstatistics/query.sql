-- name: DBStatistics :one
-- Returns the cumulative statistics reset timestamp for the current database.
-- Every usage-based check measures over the window since this point.
-- NULL means no explicit reset was recorded, which does not imply a long window:
-- an unclean shutdown or a rebuilt replica also zeroes counters without a stamp.
SELECT
  stats_reset
  , coalesce(
    extract(EPOCH FROM (now() - stats_reset)) / 86400
    , 999
  )::int AS age_days
  , (now() - stats_reset) AS age_interval
FROM pg_stat_database
WHERE datname = current_database();
