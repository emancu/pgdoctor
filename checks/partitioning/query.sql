-- name: LargeTables :many
-- Identifies all large tables (>= 10M rows) with partitioning and transient status.
-- Returns both regular and partitioned tables for unified analysis.
-- Includes activity metrics (inserts/updates/deletes) for activity-aware thresholds.
WITH inheritance_info AS (
  SELECT DISTINCT ON (i.inhrelid)
    i.inhrelid AS child_oid
    , (pn.nspname || '.' || pc.relname)::text AS parent_table
  FROM pg_inherits AS i
  INNER JOIN pg_class AS pc ON i.inhparent = pc.oid
  INNER JOIN pg_namespace AS pn ON pc.relnamespace = pn.oid
  ORDER BY i.inhrelid, i.inhparent
)

SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , ii.parent_table
  , pg_catalog.pg_table_size(c.oid) AS table_size_bytes
  , COALESCE(s.n_live_tup, 0) AS estimated_rows
  , (c.relkind = 'p') AS is_partitioned
  , (ii.parent_table IS NOT NULL) AS is_partition
  , (c.relname ~ '(outbox|inbox|_jobs?$|^oban_|logs|events?$)') AS is_transient
  , COALESCE(s.n_tup_ins, 0) AS n_tup_ins
  , COALESCE(s.n_tup_upd, 0) AS n_tup_upd
  , COALESCE(s.n_tup_del, 0) AS n_tup_del
FROM pg_catalog.pg_class AS c
INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
LEFT JOIN pg_stat_user_tables AS s ON c.oid = s.relid
LEFT JOIN inheritance_info AS ii ON c.oid = ii.child_oid
WHERE
  c.relkind IN ('r', 'p')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast', 'pgpartman', 'debezium', 'cron')
  AND COALESCE(s.n_live_tup, 0) >= 10000000;
