-- name: TableBloat :many
-- Identifies tables with high dead tuple percentages indicating vacuum issues
SELECT
  (schemaname || '.' || relname)::text AS table_name
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
  , PG_TOTAL_RELATION_SIZE(schemaname || '.' || relname) AS total_size_bytes
FROM pg_stat_user_tables
WHERE
  schemaname NOT IN ('pg_catalog', 'information_schema')
  AND n_dead_tup > 1000  -- Ignore tiny tables with few dead tuples
ORDER BY dead_tuple_percent DESC, n_dead_tup DESC;
