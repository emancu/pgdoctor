-- name: ToastStorage :many
-- Analyzes TOAST storage usage and identifies tables with large value storage
WITH toast_info AS (
  SELECT
    n.nspname::text AS schema_name
    , c.relname::text AS table_name
    , t.relname::text AS toast_table_name
    , pg_relation_size(c.oid) AS main_table_size
    , pg_relation_size(t.oid) AS toast_size
    , pg_total_relation_size(c.oid) AS total_size
    , pg_indexes_size(c.oid) AS indexes_size
    , CASE
      WHEN pg_total_relation_size(c.oid) > 0
        THEN round((pg_relation_size(t.oid)::numeric / pg_total_relation_size(c.oid)::numeric) * 100, 2)
      ELSE 0
    END AS toast_percent
    -- TOAST table statistics for bloat detection
    , coalesce(st.n_live_tup, 0) AS toast_live_tuples
    , coalesce(st.n_dead_tup, 0) AS toast_dead_tuples
  FROM pg_class AS c
  INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
  INNER JOIN pg_class AS t ON c.reltoastrelid = t.oid
  LEFT JOIN pg_stat_user_tables AS st ON t.oid = st.relid
  WHERE
    c.relkind IN ('r', 'p')
    AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
    AND c.reltoastrelid != 0
    AND pg_relation_size(t.oid) > 1048576  -- TOAST > 1MB
)

, wide_columns AS (
  SELECT
    ps.schemaname::text AS schema_name
    , ps.tablename::text AS table_name
    , ps.attname::text AS column_name
    , ps.avg_width
    , CASE
      WHEN pt.typname IN ('json', 'jsonb') THEN 'jsonb'
      WHEN pt.typname IN ('text', 'varchar', 'char', 'bpchar') THEN 'text'
      WHEN pt.typname = 'bytea' THEN 'bytea'
      ELSE 'other'
    END AS column_category
  FROM pg_stats AS ps
  INNER JOIN pg_class AS c ON ps.tablename = c.relname
  INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid AND ps.schemaname = n.nspname
  INNER JOIN pg_attribute AS pa ON c.oid = pa.attrelid AND ps.attname = pa.attname
  INNER JOIN pg_type AS pt ON pa.atttypid = pt.oid
  WHERE
    ps.schemaname NOT IN ('pg_catalog', 'information_schema')
    AND ps.avg_width > 2000  -- Likely using TOAST (threshold ~2KB)
    AND ps.avg_width IS NOT NULL
)

, column_compression AS (
  SELECT
    n.nspname::text AS schema_name
    , c.relname::text AS table_name
    , a.attname::text AS column_name
    , t.typname::text AS column_type
    , CASE a.attcompression
      WHEN 'p' THEN 'pglz'
      WHEN 'l' THEN 'lz4'
      ELSE 'default'
    END AS compression_algorithm
    , CASE a.attstorage
      WHEN 'p' THEN 'PLAIN'
      WHEN 'e' THEN 'EXTERNAL'
      WHEN 'x' THEN 'EXTENDED'
      WHEN 'm' THEN 'MAIN'
    END AS storage_strategy
  FROM pg_attribute AS a
  INNER JOIN pg_class AS c ON a.attrelid = c.oid
  INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
  INNER JOIN pg_type AS t ON a.atttypid = t.oid
  WHERE
    a.attnum > 0  -- Exclude system columns
    AND NOT a.attisdropped
    AND a.attstorage IN ('x', 'e', 'm')  -- Columns that can use TOAST
    AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
    AND c.relkind IN ('r', 'p')  -- Regular and partitioned tables
    AND t.typname IN ('text', 'varchar', 'bpchar', 'json', 'jsonb', 'bytea')  -- TOAST-able types
)

SELECT
  ti.schema_name
  , ti.table_name
  , ti.toast_table_name
  , ti.main_table_size
  , ti.toast_size
  , ti.total_size
  , ti.indexes_size
  , ti.toast_percent
  , ti.toast_live_tuples
  , ti.toast_dead_tuples
  , coalesce(
    (
      SELECT array_agg(wc.column_name || ':' || wc.avg_width::text || ':' || wc.column_category ORDER BY wc.avg_width DESC)
      FROM wide_columns AS wc
      WHERE wc.schema_name = ti.schema_name AND wc.table_name = ti.table_name
    )
    , ARRAY[]::text []
  ) AS wide_columns
  , coalesce(
    (
      SELECT
        array_agg(
          cc.column_name || ':' || cc.compression_algorithm || ':' || cc.storage_strategy || ':' || cc.column_type
          ORDER BY cc.column_name
        )
      FROM column_compression AS cc
      WHERE cc.schema_name = ti.schema_name AND cc.table_name = ti.table_name
    )
    , ARRAY[]::text []
  ) AS column_compression_info
FROM toast_info AS ti
ORDER BY ti.toast_size DESC;
