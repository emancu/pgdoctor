-- name: UuidColumnDefaults :many
-- Find UUID columns with their DEFAULT expressions to detect random UUID usage.
-- A partitioned table reports once, and a partition reports only when its default differs from its parent's default.
-- A relation counts as indexed when an index on it or on any partition below it covers the column.
-- pg_inherits instead of pg_partition_tree(), which locks every partition.
WITH RECURSIVE tree AS (
  SELECT
    ARRAY[c.oid] AS path
    , c.oid AS relid
    , NULL::oid AS parent
  FROM pg_class AS c
  WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
  UNION ALL
  SELECT
    tree.path || inh.inhrelid
    , inh.inhrelid
    , inh.inhparent
  FROM tree
  INNER JOIN pg_inherits AS inh ON tree.relid = inh.inhparent
  INNER JOIN pg_class AS pc ON inh.inhrelid = pc.oid AND pc.relispartition
)

, indexed AS (
  SELECT DISTINCT
    p.relid
    , ia.attname
  FROM tree
  CROSS JOIN LATERAL unnest(tree.path) AS p (relid)
  INNER JOIN pg_index AS i ON tree.relid = i.indrelid
  INNER JOIN pg_attribute AS ia
    ON i.indrelid = ia.attrelid AND ia.attnum = ANY(i.indkey)
)

SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , a.attname::text AS column_name
  , pg_get_expr(d.adbin, d.adrelid)::text AS default_expr
  , (ix.relid IS NOT NULL) AS has_index
FROM tree
INNER JOIN pg_class AS c ON tree.relid = c.oid
INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
INNER JOIN pg_attribute AS a ON c.oid = a.attrelid
INNER JOIN pg_type AS t ON a.atttypid = t.oid
LEFT JOIN pg_attrdef AS d ON c.oid = d.adrelid AND a.attnum = d.adnum
LEFT JOIN pg_attribute AS pa ON tree.parent = pa.attrelid AND a.attname = pa.attname
LEFT JOIN pg_attrdef AS pd ON pa.attrelid = pd.adrelid AND pa.attnum = pd.adnum
LEFT JOIN indexed AS ix ON c.oid = ix.relid AND a.attname = ix.attname
WHERE
  a.attnum > 0
  AND NOT a.attisdropped
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND c.relkind IN ('r', 'p')
  AND t.typname = 'uuid'
  AND d.adbin IS NOT NULL
  AND pg_get_expr(d.adbin, d.adrelid) IS DISTINCT FROM pg_get_expr(pd.adbin, pd.adrelid);
