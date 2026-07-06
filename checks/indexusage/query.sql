-- name: IndexUsageStats :many
-- Identifies indexes with usage statistics for health analysis.
-- Excludes: system schemas.
-- Returns data for subchecks: unused-indexes, low-usage-indexes.
SELECT
  (n.nspname || '.' || tbl.relname)::text AS table_name
  , psai.indexrelname::text AS index_name
  , c.reltuples::bigint AS num_rows
  , x.indisprimary AS is_primary
  , x.indisunique AS is_unique
  , pg_relation_size(psai.indexrelid) AS index_size_bytes
  , coalesce(psai.idx_scan, 0) AS idx_scan
  , coalesce(psai.idx_tup_read, 0) AS idx_tup_read
  , coalesce(psai.idx_tup_fetch, 0) AS idx_tup_fetch
  , coalesce(ut.n_tup_ins, 0) + coalesce(ut.n_tup_upd, 0) + coalesce(ut.n_tup_del, 0) AS table_writes
FROM pg_stat_user_indexes AS psai
INNER JOIN pg_index AS x ON psai.indexrelid = x.indexrelid
INNER JOIN pg_class AS tbl ON x.indrelid = tbl.oid
INNER JOIN pg_namespace AS n ON tbl.relnamespace = n.oid
LEFT JOIN pg_class AS c ON psai.relid = c.oid
LEFT JOIN pg_stat_user_tables AS ut ON tbl.oid = ut.relid
WHERE
  n.nspname = 'public'
ORDER BY
  pg_relation_size(psai.indexrelid) DESC;
