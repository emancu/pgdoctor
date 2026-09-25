-- name: UuidColumnDefaults :many
-- Find UUID columns with their DEFAULT expressions to detect random UUID usage.
-- A partitioned table reports once, indexed if an index on the root or any partition covers the column.
-- pg_inherits instead of pg_partition_tree(), which locks every partition.
WITH RECURSIVE tree AS (
  SELECT
    c.oid AS root
    , c.oid AS relid
  FROM pg_class AS c
  WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
  UNION ALL
  SELECT
    tree.root
    , inh.inhrelid
  FROM tree
  INNER JOIN pg_inherits AS inh ON tree.relid = inh.inhparent
  INNER JOIN pg_class AS pc ON inh.inhrelid = pc.oid AND pc.relispartition
)

, indexed AS (
  SELECT DISTINCT
    tree.root
    , ia.attname
  FROM tree
  INNER JOIN pg_index AS i ON tree.relid = i.indrelid
  INNER JOIN pg_attribute AS ia
    ON i.indrelid = ia.attrelid AND ia.attnum = ANY(i.indkey)
)

SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , a.attname::text AS column_name
  , pg_get_expr(d.adbin, d.adrelid)::text AS default_expr
  , (ix.root IS NOT NULL) AS has_index
FROM pg_attribute AS a
INNER JOIN pg_class AS c ON a.attrelid = c.oid
INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
INNER JOIN pg_type AS t ON a.atttypid = t.oid
LEFT JOIN pg_attrdef AS d ON c.oid = d.adrelid AND a.attnum = d.adnum
LEFT JOIN indexed AS ix ON c.oid = ix.root AND a.attname = ix.attname
WHERE
  a.attnum > 0
  AND NOT a.attisdropped
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND c.relkind IN ('r', 'p')
  AND NOT c.relispartition
  AND t.typname = 'uuid'
  AND d.adbin IS NOT NULL;
