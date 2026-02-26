-- name: SessionSettings :many
/*
 * PostgreSQL settings follow a precedence hierarchy:
 * 1. System defaults (postgresql.conf)
 * 2. Database-level overrides (ALTER DATABASE ... SET)
 * 3. Role-level overrides (ALTER ROLE ... SET)  <- This query checks these
 * 4. Session-level changes (SET command)
 *
 * Technical approach:
 * - Dynamically discovers application roles (login-capable, non-system)
 * - pg_db_role_setting stores role configs as text arrays: ['key=value', ...]
 * - UNNEST + split_part parse these into usable key/value pairs
 * - CROSS JOIN creates full matrix of roles × settings (shows gaps)
 * - LEFT JOIN preserves NULLs to identify which settings use defaults
 * - setdatabase = 0 filters for cluster-wide settings (not DB-specific)
 *
 * NOTE: reset_val represents the effective default (includes DB-level overrides),
 * not the raw postgresql.conf value. This is appropriate since we want to know
 * what the role actually gets vs what they override.
 */
WITH roles AS (
  SELECT r.rolname, r.oid
  FROM pg_roles AS r
  WHERE r.rolcanlogin = true
    AND r.rolsuper = false
    AND r.rolreplication = false
    AND r.rolname NOT LIKE 'pg_%'
    AND r.rolname NOT IN (
      'postgres',
      'rds_superuser', 'rdsadmin', 'rds_replication',
      'cloudsqladmin', 'cloudsqlagent', 'cloudsqlsuperuser',
      'azure_superuser', 'azure_pg_admin', 'azuresu'
    )
)

, settings AS (
  SELECT
    s.name
    , s.reset_val
    , s.unit
  FROM pg_settings AS s
  WHERE s.name IN (
    'statement_timeout'
    , 'idle_in_transaction_session_timeout'
    , 'transaction_timeout'
    , 'log_min_duration_statement'
  )
)

, role_configs AS (
  SELECT
    r.rolname
    , unnest(coalesce(
      (
        SELECT drs.setconfig
        FROM pg_db_role_setting AS drs
        WHERE
          drs.setrole = r.oid
          AND drs.setdatabase = 0
      )
      , ARRAY[]::text []
    )) AS config_setting
  FROM roles AS r
)

, parsed_configs AS (
  SELECT
    rolname
    , split_part(config_setting, '=', 1) AS param_name
    , split_part(config_setting, '=', 2) AS param_value
  FROM role_configs
)

SELECT
  r.rolname::varchar AS role_name
  , s.name::varchar AS setting_name
  , s.reset_val AS system_default
  , s.unit
  , coalesce(pc.param_value, s.reset_val) AS setting_value
  , CASE
    WHEN pc.param_value IS NOT NULL THEN 'OVERRIDE'
    ELSE 'DEFAULT'
  END AS status
FROM roles AS r
CROSS JOIN settings AS s
LEFT JOIN parsed_configs AS pc
  ON
    r.rolname = pc.rolname
    AND s.name = pc.param_name
ORDER BY r.rolname, s.name;
