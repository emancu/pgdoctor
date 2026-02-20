-- name: UuidColumnsAsString :many
-- Identifies columns that appear to store UUIDs but use string types.
SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , a.attname::text AS column_name
  , t.typname::text AS column_type
  , pg_catalog.pg_table_size(c.oid) AS table_size_bytes
FROM pg_catalog.pg_attribute AS a
INNER JOIN pg_catalog.pg_class AS c ON a.attrelid = c.oid
INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
INNER JOIN pg_catalog.pg_type AS t ON a.atttypid = t.oid
WHERE
  a.attnum > 0
  AND NOT a.attisdropped
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND c.relkind IN ('r', 'p')
  AND t.typname IN ('varchar', 'text', 'bpchar', 'char')
  AND a.attname ~* 'uuid'
ORDER BY pg_catalog.pg_table_size(c.oid) DESC;
