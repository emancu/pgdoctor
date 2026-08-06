-- name: QueryStatisticsAvailability :one
-- pg_stat_statements registers its GUCs only when preloaded, so a
-- pg_stat_statements.max row means "loaded". shared_preload_libraries cannot answer
-- this: it is superuser-only and reads as absent for unprivileged roles.
-- to_regclass returns NULL instead of erroring when the extension was created in a
-- schema outside search_path.
SELECT
  EXISTS (SELECT 1 FROM pg_catalog.pg_settings AS s WHERE s.name = 'pg_stat_statements.max') AS is_loaded
  , EXISTS (SELECT 1 FROM pg_catalog.pg_extension AS e WHERE e.extname = 'pg_stat_statements') AS is_installed
  , (to_regclass('pg_stat_statements_info') IS NOT NULL) AS is_reachable;

-- name: QueryStatisticsWindow :one
-- Separate query because naming pg_stat_statements_info fails to parse when the
-- extension is absent, even inside a branch that never runs. Call it only after
-- QueryStatisticsAvailability reports the view is usable.
SELECT
  stats_reset
  , extract(EPOCH FROM (now() - stats_reset))::bigint AS age_seconds
FROM pg_stat_statements_info;
