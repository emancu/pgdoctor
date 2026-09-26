-- name: TableBloat :many
-- Identifies tables with high dead tuple percentages indicating vacuum issues
SELECT
  (schemaname || '.' || s.relname)::text AS table_name
  , n_live_tup AS live_tuples
  , n_dead_tup AS dead_tuples
  , last_autovacuum
  , last_vacuum
  , last_autoanalyze
  , last_analyze
  , autovacuum_count
  , vacuum_count
  , n_mod_since_analyze AS modifications_since_analyze
  , CASE
    WHEN n_live_tup + n_dead_tup > 0
      THEN ROUND((n_dead_tup::numeric / (n_live_tup + n_dead_tup)::numeric) * 100, 2)
    ELSE 0
  END AS dead_tuple_percent
  , pg_total_relation_size(relid) AS total_size_bytes
  , NULLIF(c.relpages, 0)::bigint * current_setting('block_size')::bigint AS heap_size_bytes
FROM pg_stat_user_tables AS s
INNER JOIN pg_class AS c ON c.oid = s.relid
WHERE
  schemaname NOT IN ('pg_catalog', 'information_schema')
  AND n_dead_tup > 1000  -- Ignore tiny tables with few dead tuples
ORDER BY dead_tuple_percent DESC, n_dead_tup DESC;
