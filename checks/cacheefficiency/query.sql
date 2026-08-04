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

-- name: IndexCacheEfficiency :many
-- Per-index buffer cache hit ratios. Low ratios on large indexes indicate frequent disk I/O.
SELECT
  (schemaname || '.' || indexrelname)::text AS index_name
  , pg_relation_size(indexrelid) AS index_size_bytes
  , CASE
    WHEN coalesce(idx_blks_hit, 0) + coalesce(idx_blks_read, 0) = 0 THEN NULL
    ELSE round(100.0 * idx_blks_hit / (idx_blks_hit + idx_blks_read), 2)
  END AS cache_hit_ratio
FROM pg_statio_user_indexes
WHERE
  schemaname = 'public'
  AND pg_relation_size(indexrelid) > 10 * 1024 * 1024
ORDER BY pg_relation_size(indexrelid) DESC;
