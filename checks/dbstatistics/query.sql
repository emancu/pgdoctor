-- name: DBStatistics :one
-- Returns the cumulative statistics reset timestamp for the current database.
-- Every usage-based check measures over the window since this point.
-- NULL means no explicit reset was recorded, which does not imply a long window:
-- an unclean shutdown or a rebuilt replica also zeroes counters without a stamp.
--
-- The age is computed here rather than in Go so it is measured against the server's
-- clock: stats_reset comes from the server, and differencing it against the CLI
-- host's clock would skew both the reported window and the maturity threshold.
SELECT
  stats_reset
  , extract(EPOCH FROM (now() - stats_reset))::bigint AS age_seconds
FROM pg_stat_database
WHERE datname = current_database();
