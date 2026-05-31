-- name: BrokenIndexes :many
-- Reports indexes that are NOT indisvalid, classifying each as either a
-- genuinely-broken index (is_leftover = false -> FAIL) or an abandoned
-- REINDEX CONCURRENTLY transient (_ccnew/_ccold family, is_leftover = true -> WARN).
-- Excludes indexes currently being built: during CREATE INDEX CONCURRENTLY and
-- REINDEX INDEX CONCURRENTLY, pg_stat_progress_create_index.index_relid points
-- to the new (invalid) index OID, so an active progress row means "in flight,
-- not broken" (verified against PG15/PG16 ReindexRelationConcurrently).
-- Schema exclusion is intentionally narrower than duplicateindexes: an invalid
-- index in cron/pgpartman/debezium is a real operational problem worth reporting,
-- whereas a *duplicate* design choice there is not ours to fix.
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
