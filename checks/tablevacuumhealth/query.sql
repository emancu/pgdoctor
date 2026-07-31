-- name: TableVacuumHealth :many
-- Returns all tables with vacuum-related health metrics.
-- Used by subchecks: autovacuum-disabled, large-table-defaults, vacuum-stale.
SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , s.last_autovacuum
  , COALESCE(s.n_live_tup, c.reltuples::bigint) AS estimated_rows
  , PG_TOTAL_RELATION_SIZE(c.oid) AS table_size_bytes
  , COALESCE(s.n_dead_tup, 0) AS n_dead_tup
  , COALESCE(s.vacuum_count, 0) AS vacuum_count
  , COALESCE(s.autovacuum_count, 0) AS autovacuum_count
  , ARRAY_TO_STRING(c.reloptions, ',') AS reloptions
  , GREATEST(s.last_vacuum, s.last_autovacuum) AS last_vacuum_any
  , GREATEST(s.last_analyze, s.last_autoanalyze) AS last_analyze_any
  , COALESCE(s.n_mod_since_analyze, 0) AS n_mod_since_analyze
  , COALESCE(s.analyze_count, 0) AS analyze_count
  , COALESCE(s.autoanalyze_count, 0) AS autoanalyze_count
  -- n_ins_since_vacuum is PG14+; older versions COALESCE to 0.
  , COALESCE(s.n_ins_since_vacuum, 0) AS n_ins_since_vacuum
FROM pg_class AS c
INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
LEFT JOIN pg_stat_user_tables AS s ON c.oid = s.relid
WHERE
  c.relkind IN ('r', 'p')
  AND n.nspname = 'public'
ORDER BY COALESCE(s.n_live_tup, c.reltuples::bigint) DESC;
