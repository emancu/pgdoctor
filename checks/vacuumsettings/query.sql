-- name: VacuumSettings :many
-- noqa: disable=RF04
SELECT
  name::varchar
  , setting
  , unit
FROM pg_settings
WHERE
  name IN (
    'autovacuum_analyze_scale_factor'
    , 'autovacuum_max_workers'
    , 'autovacuum_vacuum_scale_factor'
    , 'maintenance_work_mem'
    , 'max_connections'
    , 'vacuum_cost_delay'
    , 'vacuum_cost_limit'
    , 'work_mem'
  )
UNION
SELECT
  'active_connections' AS name
  , COUNT(*)::varchar AS setting
  , NULL AS unit
FROM pg_stat_activity;
