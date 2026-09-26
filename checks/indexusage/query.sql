-- name: IndexUsageStats :many
-- Excludes: system schemas and temporary tables. Returns data for subchecks: unused-indexes, low-usage-indexes.
SELECT
  (n.nspname || '.' || tbl.relname)::text AS table_name
  , psai.indexrelname::text AS index_name
  , x.indisprimary AS is_primary
  , x.indisunique AS is_unique
  , pg_relation_size(psai.indexrelid) AS index_size_bytes
  , coalesce(psai.idx_scan, 0) AS idx_scan
  , coalesce(ut.n_tup_ins, 0) + coalesce(ut.n_tup_upd, 0) + coalesce(ut.n_tup_del, 0) AS table_writes
  , (SELECT stats_reset FROM pg_stat_database WHERE datname = current_database())::timestamptz AS stats_reset
  -- Counters survive a clean restart, so with no reset recorded the uptime is a lower bound of the window.
  , (
    SELECT extract(EPOCH FROM (now() - coalesce(stats_reset, pg_postmaster_start_time())))::bigint
    FROM pg_stat_database WHERE datname = current_database()
  ) AS stats_age_seconds
FROM pg_stat_user_indexes AS psai
INNER JOIN pg_index AS x ON psai.indexrelid = x.indexrelid
INNER JOIN pg_class AS tbl ON x.indrelid = tbl.oid
INNER JOIN pg_namespace AS n ON tbl.relnamespace = n.oid
LEFT JOIN pg_stat_user_tables AS ut ON tbl.oid = ut.relid
WHERE
  n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND tbl.relpersistence <> 't'
ORDER BY
  pg_relation_size(psai.indexrelid) DESC;
