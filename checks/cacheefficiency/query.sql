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
-- Per-index buffer cache hit ratios with scan frequency. Low ratios on large,
-- frequently-scanned indexes indicate hot-path disk I/O.
SELECT
  (psio.schemaname || '.' || psio.indexrelname)::text AS index_name
  , pg_relation_size(psio.indexrelid) AS index_size_bytes
  , coalesce(psi.idx_scan, 0) AS idx_scan
  , CASE
    WHEN coalesce(psio.idx_blks_hit, 0) + coalesce(psio.idx_blks_read, 0) = 0 THEN NULL
    ELSE round(100.0 * psio.idx_blks_hit / (psio.idx_blks_hit + psio.idx_blks_read), 2)
  END AS cache_hit_ratio
  , (SELECT stats_reset FROM pg_stat_database WHERE datname = current_database())::timestamptz AS stats_reset
FROM pg_statio_user_indexes AS psio
INNER JOIN pg_stat_user_indexes AS psi ON psio.indexrelid = psi.indexrelid
WHERE
  psio.schemaname = 'public'
  AND pg_relation_size(psio.indexrelid) >= 500 * 1024 * 1024
ORDER BY pg_relation_size(psio.indexrelid) DESC;
