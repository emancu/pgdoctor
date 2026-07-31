-- name: IndexUsageStats :many
-- Excludes: system schemas. Returns data for subchecks: unused-indexes, low-usage-indexes, index-cache-ratio.
SELECT
  (n.nspname || '.' || tbl.relname)::text AS table_name
  , psai.indexrelname::text AS index_name
  , x.indisprimary AS is_primary
  , x.indisunique AS is_unique
  , pg_relation_size(psai.indexrelid) AS index_size_bytes
  , coalesce(psai.idx_scan, 0) AS idx_scan
  , coalesce(ut.n_tup_ins, 0) + coalesce(ut.n_tup_upd, 0) + coalesce(ut.n_tup_del, 0) AS table_writes
  , CASE
    WHEN coalesce(psaio.idx_blks_hit, 0) + coalesce(psaio.idx_blks_read, 0) = 0 THEN NULL
    ELSE round(
      100.0 * psaio.idx_blks_hit / (psaio.idx_blks_hit + psaio.idx_blks_read)
      , 2
    )
  END AS cache_hit_ratio
  , (SELECT stats_reset FROM pg_stat_database WHERE datname = current_database())::timestamptz AS stats_reset
FROM pg_stat_user_indexes AS psai
INNER JOIN pg_index AS x ON psai.indexrelid = x.indexrelid
INNER JOIN pg_class AS tbl ON x.indrelid = tbl.oid
INNER JOIN pg_namespace AS n ON tbl.relnamespace = n.oid
LEFT JOIN pg_stat_user_tables AS ut ON tbl.oid = ut.relid
LEFT JOIN pg_statio_user_indexes AS psaio ON psai.indexrelid = psaio.indexrelid
WHERE
  n.nspname = 'public'
ORDER BY
  pg_relation_size(psai.indexrelid) DESC;
