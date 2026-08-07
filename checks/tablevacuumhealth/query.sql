-- name: TableVacuumHealth :many
-- Returns all tables with vacuum-related health metrics.
-- Used by subchecks: autovacuum-disabled, large-table-defaults, vacuum-stale.
SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , s.last_autovacuum
  , COALESCE(s.n_live_tup, c.reltuples::bigint) AS estimated_rows
  -- Lock-free size estimate from pg_class instead of PG_TOTAL_RELATION_SIZE(),
  -- which takes an AccessShareLock: a new AccessShareLock request queues behind a
  -- *waiting* AccessExclusiveLock, so it makes this check time out during a DDL
  -- pile-up. relpages is only refreshed by VACUUM/ANALYZE, so it is stale by
  -- definition and 0 on a never-vacuumed relation.
  , (c.relpages + COALESCE(t.relpages, 0) + COALESCE(i.index_pages, 0))::BIGINT
    * CURRENT_SETTING('block_size')::BIGINT AS table_size_bytes
  , COALESCE(s.n_dead_tup, 0) AS n_dead_tup
  , COALESCE(s.vacuum_count, 0) AS vacuum_count
  , COALESCE(s.autovacuum_count, 0) AS autovacuum_count
  , ARRAY_TO_STRING(c.reloptions, ',') AS reloptions
  -- NULL means never.
  , EXTRACT(EPOCH FROM (now() - GREATEST(s.last_vacuum, s.last_autovacuum)))::bigint AS last_vacuum_age_seconds
  , EXTRACT(EPOCH FROM (now() - GREATEST(s.last_analyze, s.last_autoanalyze)))::bigint AS last_analyze_age_seconds
  , COALESCE(s.n_mod_since_analyze, 0) AS n_mod_since_analyze
  , COALESCE(s.analyze_count, 0) AS analyze_count
  , COALESCE(s.autoanalyze_count, 0) AS autoanalyze_count
  -- n_ins_since_vacuum is PG14+; older versions COALESCE to 0.
  , COALESCE(s.n_ins_since_vacuum, 0) AS n_ins_since_vacuum
FROM pg_class AS c
INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
LEFT JOIN pg_stat_user_tables AS s ON c.oid = s.relid
LEFT JOIN pg_class AS t ON t.oid = c.reltoastrelid
LEFT JOIN LATERAL (
  SELECT SUM(ic.relpages)::BIGINT AS index_pages
  FROM pg_index AS x
  INNER JOIN pg_class AS ic ON ic.oid = x.indexrelid
  WHERE x.indrelid IN (c.oid, c.reltoastrelid)
) AS i ON TRUE
WHERE
  c.relkind IN ('r', 'p')
  AND n.nspname = 'public'
ORDER BY COALESCE(s.n_live_tup, c.reltuples::bigint) DESC;
