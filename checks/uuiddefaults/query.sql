-- name: UuidColumnDefaults :many
-- Find UUID columns with their DEFAULT expressions to detect random UUID usage.
WITH indexed_columns AS (
  SELECT
    i.indrelid AS table_oid
    , unnest(i.indkey) AS column_num
  FROM pg_index AS i
)

SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , a.attname::text AS column_name
  , pg_get_expr(d.adbin, d.adrelid)::text AS default_expr
  , (idx.column_num IS NOT NULL) AS has_index
FROM pg_attribute AS a
INNER JOIN pg_class AS c ON a.attrelid = c.oid
INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
INNER JOIN pg_type AS t ON a.atttypid = t.oid
LEFT JOIN pg_attrdef AS d ON c.oid = d.adrelid AND a.attnum = d.adnum
LEFT JOIN indexed_columns AS idx
  ON c.oid = idx.table_oid AND a.attnum = idx.column_num
WHERE
  a.attnum > 0
  AND NOT a.attisdropped
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND c.relkind IN ('r', 'p')
  AND t.typname = 'uuid'
  AND d.adbin IS NOT NULL;
