SELECT
    name::varchar
    , setting
    , unit
FROM pg_settings
WHERE name IN (
    'autovacuum_analyze_scale_factor'
    , 'autovacuum_max_workers'
    , 'autovacuum_vacuum_scale_factor'
    , 'maintenance_work_mem'
    , 'vacuum_cost_delay'
    , 'vacuum_cost_limit'
    , 'work_mem'
)
