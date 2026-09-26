-- name: InvalidPrimaryKeyTypes :many
-- Identifies tables with integer primary keys (int2/int4) that should use bigint.
WITH pk_columns AS (
  SELECT
    con.conrelid AS table_oid
    , UNNEST(con.conkey) AS column_num
  FROM pg_catalog.pg_constraint AS con
  WHERE con.contype = 'p'
)

, pk_tables AS (
  SELECT
    n.nspname::text AS schema_name
    , c.relname::text AS table_name
    , a.attname::text AS column_name
    , a.attnum AS column_num
    , t.typname::text AS column_type
    , c.oid AS table_oid
    , COALESCE(pg_stat_get_live_tuples(c.oid), 0)::bigint AS estimated_rows
    , CASE t.typname
      WHEN 'int2' THEN 32767::bigint
      WHEN 'int4' THEN 2147483647::bigint
    END AS type_max_value
  FROM pk_columns AS pk
  INNER JOIN pg_catalog.pg_class AS c ON pk.table_oid = c.oid
  INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
  INNER JOIN pg_catalog.pg_attribute AS a
    ON
      pk.table_oid = a.attrelid
      AND pk.column_num = a.attnum
  INNER JOIN pg_catalog.pg_type AS t ON a.atttypid = t.oid
  WHERE
    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pgpartman', 'pgjobmon', 'cron')
    AND t.typname IN ('int2', 'int4')
    AND NOT EXISTS (
      SELECT 1 FROM pg_inherits AS inh
      WHERE inh.inhrelid = c.oid
    )
)

-- serial columns own their sequence with deptype 'a', IDENTITY columns with 'i'.
, sequence_owners AS (
  SELECT
    d.refobjid AS table_oid
    , d.refobjsubid AS column_num
    , d.objid AS sequence_oid
  FROM pg_catalog.pg_depend AS d
  INNER JOIN pg_catalog.pg_sequence AS s ON d.objid = s.seqrelid
  WHERE
    d.classid = 'pg_catalog.pg_class'::regclass
    AND d.refclassid = 'pg_catalog.pg_class'::regclass
    AND d.deptype IN ('a', 'i')
)

, pk_sequences AS (
  SELECT
    p.schema_name
    , p.table_name
    , p.column_name
    , p.column_type
    , p.estimated_rows
    , p.type_max_value
    , CASE
      WHEN has_sequence_privilege(so.sequence_oid, 'SELECT,USAGE')
        THEN pg_sequence_last_value(so.sequence_oid::regclass)
    END AS sequence_current
  FROM pk_tables AS p
  LEFT JOIN sequence_owners AS so
    ON
      p.table_oid = so.table_oid
      AND p.column_num = so.column_num
)

, pk_with_usage AS (
  SELECT
    (p.schema_name || '.' || p.table_name)::text AS table_name
    , p.column_name
    , p.column_type
    , p.estimated_rows
    , p.sequence_current
    , p.type_max_value
    , CASE
      WHEN p.sequence_current IS NOT NULL AND p.type_max_value > 0
        THEN p.sequence_current::numeric / p.type_max_value::numeric
      WHEN p.estimated_rows > 0 AND p.type_max_value > 0
        THEN p.estimated_rows::numeric / p.type_max_value::numeric
      ELSE
        0::numeric
    END AS usage_pct
  FROM pk_sequences AS p
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
