-- name: InvalidPrimaryKeyTypes :many
-- Identifies tables with integer primary keys (int2/int4) that should use bigint.
WITH pk_tables AS (
  SELECT
    n.nspname::text AS schema_name
    , c.relname::text AS table_name
    , a.attname::text AS column_name
    , a.attnum AS column_num
    , t.typname::text AS column_type
    , c.oid AS table_oid
    , COALESCE(s.n_live_tup, 0)::bigint AS estimated_rows
    , CASE t.typname
      WHEN 'int2' THEN 32767::bigint
      WHEN 'int4' THEN 2147483647::bigint
    END AS type_max_value
  FROM pg_catalog.pg_constraint AS con
  INNER JOIN pg_catalog.pg_class AS c ON con.conrelid = c.oid
  INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
  INNER JOIN pg_catalog.pg_attribute AS a
    ON
      con.conrelid = a.attrelid
      AND a.attnum = ANY(con.conkey)
  INNER JOIN pg_catalog.pg_type AS t ON a.atttypid = t.oid
  LEFT JOIN pg_stat_user_tables AS s ON c.oid = s.relid
  WHERE
    con.contype = 'p'
    AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pgpartman', 'pgjobmon', 'cron')
    AND t.typname IN ('int2', 'int4')
    AND NOT EXISTS (
      SELECT 1 FROM pg_inherits AS inh
      WHERE inh.inhrelid = c.oid
    )
)

, sequence_values AS (
  SELECT
    d.refobjid AS table_oid
    , d.refobjsubid AS column_num
    , seq.last_value::bigint AS sequence_current
  FROM pg_depend AS d
  INNER JOIN pg_class AS seq_class ON d.objid = seq_class.oid
  INNER JOIN pg_sequences AS seq ON seq_class.relname = seq.sequencename
  WHERE
    d.deptype = 'a'
    AND seq_class.relkind = 'S'
)

, pk_with_usage AS (
  SELECT
    (p.schema_name || '.' || p.table_name)::text AS table_name
    , p.column_name
    , p.column_type
    , p.estimated_rows
    , sv.sequence_current
    , p.type_max_value
    , CASE
      WHEN sv.sequence_current IS NOT NULL AND p.type_max_value > 0
        THEN sv.sequence_current::numeric / p.type_max_value::numeric
      WHEN p.estimated_rows > 0 AND p.type_max_value > 0
        THEN p.estimated_rows::numeric / p.type_max_value::numeric
      ELSE
        0::numeric
    END AS usage_pct
  FROM pk_tables AS p
  LEFT JOIN sequence_values AS sv
    ON
      p.table_oid = sv.table_oid
      AND p.column_num = sv.column_num
)

SELECT
  table_name
  , column_name
  , column_type
  , estimated_rows
  , sequence_current
  , type_max_value
  , usage_pct
FROM pk_with_usage
ORDER BY
  usage_pct DESC NULLS LAST
  , estimated_rows DESC NULLS LAST;
