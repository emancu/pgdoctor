-- name: QueryStatisticsAvailability :one
-- Distinguishes the three ways pg_stat_statements can be half-present.
--
-- The extension's GUCs are only registered when its library is preloaded, so the
-- existence of a pg_stat_statements.max row means "loaded", independent of whether
-- CREATE EXTENSION has run. Both catalogs are world-readable; shared_preload_libraries
-- is GUC_SUPERUSER_ONLY and is silently omitted from pg_settings for unprivileged
-- roles, so it must not be used to answer this.
SELECT
  EXISTS (
    SELECT 1 FROM pg_catalog.pg_settings AS s WHERE s.name = 'pg_stat_statements.max'
  ) AS is_loaded
  , EXISTS (
    SELECT 1 FROM pg_catalog.pg_extension AS e WHERE e.extname = 'pg_stat_statements'
  ) AS is_installed;

-- name: QueryStatisticsWindow :one
-- Raises "pg_stat_statements must be loaded via shared_preload_libraries" when the
-- extension is created but not preloaded, so call it only once
-- QueryStatisticsAvailability reports both.
SELECT stats_reset
FROM pg_stat_statements_info;
