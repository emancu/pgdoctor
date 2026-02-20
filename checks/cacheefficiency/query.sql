-- name: DatabaseCacheEfficiency :one
-- Returns database-wide buffer cache hit ratio.
-- Low ratios indicate shared_buffers too small or working set exceeds memory.
SELECT
  blks_hit
  , blks_read
  , stats_reset
  , CASE
    WHEN blks_hit + blks_read = 0 THEN NULL
    ELSE round(100.0 * blks_hit / (blks_hit + blks_read), 2)
  END AS cache_hit_ratio
  , coalesce(
    extract(EPOCH FROM (now() - stats_reset)) / 86400
    , 999
  ) AS stats_age_days
FROM pg_stat_database
WHERE datname = current_database();
