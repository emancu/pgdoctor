-- name: DuplicateIndexes :many
-- Identifies exact and prefix duplicate indexes on the same table.
-- Uses index column positions (indkey) for prefix detection.
-- Excludes: system schemas, invalid indexes, expression/partial indexes for prefix check.
WITH index_columns AS (
  SELECT
    idx.indexrelid
    , idx.indrelid
    , i.relname AS index_name
    , t.relname AS table_name
    , n.nspname AS schema_name
    , idx.indkey::int [] AS column_positions
    , idx.indnkeyatts AS num_key_columns
    -- Extract column list as array for prefix comparison
    , pg_get_indexdef(idx.indexrelid) AS index_def
    , pg_relation_size(i.oid) AS index_size_bytes
    -- Detect expression/partial indexes (cannot reliably compare)
    , (idx.indexprs IS NOT NULL) AS is_expression_index
    , (idx.indpred IS NOT NULL) AS is_partial_index
  FROM pg_index AS idx
  INNER JOIN pg_class AS i ON idx.indexrelid = i.oid
  INNER JOIN pg_class AS t ON idx.indrelid = t.oid
  INNER JOIN pg_namespace AS n ON t.relnamespace = n.oid
  WHERE
    i.relkind = 'i'
    AND idx.indisvalid
    AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast', 'cron', 'pgpartman', 'debezium')
)

, exact_duplicates AS (
  -- Find indexes with identical definitions (after removing index name)
  SELECT
    a.schema_name
    , a.table_name
    , a.index_name AS index_name_a
    , b.index_name AS index_name_b
    , a.index_size_bytes AS size_a
    , b.index_size_bytes AS size_b
    , a.index_def AS definition_a
    , 'exact' AS duplicate_type
  FROM index_columns AS a
  INNER JOIN index_columns AS b ON
    a.indrelid = b.indrelid
    AND a.indexrelid < b.indexrelid
    AND regexp_replace(a.index_def, 'INDEX \S+ ON', 'INDEX ON', 'g')
    = regexp_replace(b.index_def, 'INDEX \S+ ON', 'INDEX ON', 'g')
)

, prefix_duplicates AS (
  -- Find indexes where one is a left-prefix of another
  -- e.g., (a) is prefix of (a, b)
  SELECT
    a.schema_name
    , a.table_name
    , a.index_name AS index_name_a
    , b.index_name AS index_name_b
    , a.index_size_bytes AS size_a
    , b.index_size_bytes AS size_b
    , a.index_def AS definition_a
    , 'prefix' AS duplicate_type
  FROM index_columns AS a
  INNER JOIN index_columns AS b ON
    a.indrelid = b.indrelid
    AND a.indexrelid <> b.indexrelid
    AND a.num_key_columns < b.num_key_columns
    AND a.column_positions = b.column_positions[0:a.num_key_columns]
    AND NOT a.is_expression_index
    AND NOT b.is_expression_index
    AND NOT a.is_partial_index
    AND NOT b.is_partial_index
)

SELECT
  (schema_name || '.' || table_name)::text AS table_name
  , index_name_a::text
  , index_name_b::text
  , size_a
  , size_b
  , definition_a::text
  , duplicate_type::text
FROM (
  SELECT
    schema_name
    , table_name
    , index_name_a
    , index_name_b
    , size_a
    , size_b
    , definition_a
    , duplicate_type
  FROM exact_duplicates
  UNION ALL
  SELECT
    schema_name
    , table_name
    , index_name_a
    , index_name_b
    , size_a
    , size_b
    , definition_a
    , duplicate_type
  FROM prefix_duplicates
) AS all_duplicates
ORDER BY
  size_a + size_b DESC;
