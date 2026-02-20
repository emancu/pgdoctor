-- name: SequenceHealth :many
-- Identifies sequences approaching their maximum values and integer columns that should be bigint
WITH sequence_info AS (
  SELECT
    s.schemaname::text AS schema_name
    , s.sequencename::text AS sequence_name
    , s.data_type::text AS seq_data_type
    , s.max_value
    , s.increment_by
    , s.cycle AS is_cyclic
    , COALESCE(s.last_value, s.start_value) AS current_value
    , CASE
      WHEN s.max_value > 0 AND COALESCE(s.last_value, s.start_value) > 0
        THEN (COALESCE(s.last_value, s.start_value)::numeric / s.max_value::numeric) * 100
      ELSE 0
    END AS usage_percent
    , (s.max_value - COALESCE(s.last_value, s.start_value)) / NULLIF(s.increment_by, 0) AS remaining_values
  FROM pg_sequences AS s
  WHERE s.schemaname NOT IN ('pg_catalog', 'information_schema')
)

-- Find columns that own sequences (SERIAL/BIGSERIAL columns)
, sequence_owners AS (
  SELECT
    seq_ns.nspname::text AS seq_schema
    , seq_class.relname::text AS seq_name
    , tbl_ns.nspname::text AS table_schema
    , tbl_class.relname::text AS table_name
    , tbl_class.oid AS table_oid
    , attr.attname::text AS column_name
    , attr.attnum AS column_num
    , FORMAT_TYPE(attr.atttypid, attr.atttypmod)::text AS column_type
    , CASE FORMAT_TYPE(attr.atttypid, attr.atttypmod)
      WHEN 'integer' THEN 2147483647::bigint
      WHEN 'smallint' THEN 32767::bigint
      WHEN 'bigint' THEN 9223372036854775807::bigint
    END AS column_max_value
  FROM pg_depend AS dep
  INNER JOIN pg_class AS seq_class ON dep.objid = seq_class.oid AND seq_class.relkind = 'S'
  INNER JOIN pg_namespace AS seq_ns ON seq_class.relnamespace = seq_ns.oid
  INNER JOIN pg_class AS tbl_class ON dep.refobjid = tbl_class.oid AND tbl_class.relkind = 'r'
  INNER JOIN pg_namespace AS tbl_ns ON tbl_class.relnamespace = tbl_ns.oid
  INNER JOIN pg_attribute AS attr ON tbl_class.oid = attr.attrelid AND dep.refobjsubid = attr.attnum
  WHERE (
    dep.deptype = 'a'  -- Auto dependency (SERIAL creates this)
    OR attr.attidentity IN ('a', 'd')
  )  -- IDENTITY columns (PostgreSQL 10+)
  AND seq_ns.nspname NOT IN ('pg_catalog', 'information_schema')
)

-- Check if columns are primary keys
, primary_keys AS (
  SELECT
    con.conrelid AS table_oid
    , UNNEST(con.conkey) AS column_num
  FROM pg_constraint AS con
  WHERE con.contype = 'p'  -- Primary key
)

-- Count foreign keys referencing each column (as the referenced/target column)
, fk_references AS (
  SELECT
    con.confrelid AS referenced_table_oid
    , UNNEST(con.confkey) AS referenced_column_num
    , COUNT(*) AS fk_count
  FROM pg_constraint AS con
  WHERE con.contype = 'f'  -- Foreign key
  GROUP BY con.confrelid, UNNEST(con.confkey)
)

SELECT
  si.schema_name
  , si.sequence_name
  , si.seq_data_type
  , si.current_value
  , si.max_value
  , si.increment_by
  , si.is_cyclic
  , si.remaining_values
  , ROUND(si.usage_percent::numeric, 2) AS usage_percent
  , COALESCE(so.table_name, '') AS table_name
  , COALESCE(so.column_name, '') AS column_name
  , COALESCE(so.column_type, '') AS column_type
  , COALESCE(so.column_max_value, 0) AS column_max_value
  -- Flag if sequence can generate values that exceed column type
  , (so.column_max_value IS NOT NULL AND si.max_value > so.column_max_value) AS sequence_exceeds_column
  -- Flag if this is an integer column that should probably be bigint
  , (so.column_type != 'bigint' AND si.usage_percent > 50) AS should_be_bigint
  -- Flag if column is a primary key
  , (pk.table_oid IS NOT NULL) AS is_primary_key
  -- Count of foreign keys referencing this column
  , COALESCE(fkr.fk_count, 0) AS fk_reference_count

FROM sequence_info AS si
LEFT JOIN sequence_owners AS so
  ON
    si.schema_name = so.seq_schema
    AND si.sequence_name = so.seq_name
LEFT JOIN primary_keys AS pk
  ON
    so.table_oid = pk.table_oid
    AND so.column_num = pk.column_num
LEFT JOIN fk_references AS fkr
  ON
    so.table_oid = fkr.referenced_table_oid
    AND so.column_num = fkr.referenced_column_num
ORDER BY si.usage_percent DESC, si.remaining_values ASC;
