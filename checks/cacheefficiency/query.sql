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
-- Per-index buffer cache hit ratios with scan rank and share across all user
-- indexes. Rank/share are computed before the size floor so a small index still
-- counts toward the traffic universe; the outer filters only limit what we list.
WITH ranked AS (
  SELECT
    indexrelid
    , coalesce(idx_scan, 0) AS idx_scan
    , rank() OVER (ORDER BY coalesce(idx_scan, 0) DESC) AS scan_rank
    , coalesce(idx_scan, 0)::numeric / NULLIF(sum(coalesce(idx_scan, 0)) OVER (), 0) AS scan_share
  FROM pg_stat_user_indexes
)
SELECT
  (psio.schemaname || '.' || psio.indexrelname)::text AS index_name
  , pg_relation_size(psio.indexrelid) AS index_size_bytes
  , ranked.idx_scan AS idx_scan
  , ranked.scan_rank AS scan_rank
  , ranked.scan_share AS scan_share
  , CASE
    WHEN coalesce(psio.idx_blks_hit, 0) + coalesce(psio.idx_blks_read, 0) = 0 THEN NULL
    ELSE round(100.0 * psio.idx_blks_hit / (psio.idx_blks_hit + psio.idx_blks_read), 2)
  END AS cache_hit_ratio
FROM pg_statio_user_indexes AS psio
INNER JOIN ranked ON psio.indexrelid = ranked.indexrelid
WHERE
  psio.schemaname = 'public'
  AND pg_relation_size(psio.indexrelid) >= 500 * 1024 * 1024
ORDER BY pg_relation_size(psio.indexrelid) DESC;
