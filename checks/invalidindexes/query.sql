-- name: BrokenIndexes :many
-- Invalid indexes, flagging _ccnew/_ccold REINDEX CONCURRENTLY leftovers via
-- is_leftover. Excludes indexes a concurrent build is still working on (their
-- index_relid is in pg_stat_progress_create_index): in flight, not broken.
SELECT
  n.nspname::text AS schema_name
  , tbl.relname::text AS table_name
  , idx.relname::text AS index_name
  , (idx.relname ~ '_cc(new|old)[0-9]*$') AS is_leftover
FROM pg_index AS i
INNER JOIN pg_class AS idx ON i.indexrelid = idx.oid
INNER JOIN pg_class AS tbl ON i.indrelid = tbl.oid
INNER JOIN pg_namespace AS n ON tbl.relnamespace = n.oid
WHERE NOT i.indisvalid
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND NOT EXISTS (
    SELECT 1 FROM pg_stat_progress_create_index AS p
    WHERE p.index_relid = i.indexrelid
  )
ORDER BY is_leftover, n.nspname, tbl.relname, idx.relname;
