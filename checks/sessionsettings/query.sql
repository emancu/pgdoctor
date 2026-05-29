-- name: SessionSettings :many
/*
 * PostgreSQL GUC precedence for ALTER ROLE / ALTER DATABASE settings,
 * from most specific to least:
 *
 *   1. ALTER ROLE r IN DATABASE d SET ...     (setrole=r.oid, setdatabase=d.oid)
 *   2. ALTER ROLE r SET ...                   (setrole=r.oid, setdatabase=0)
 *   3. ALTER DATABASE d SET ... / ALTER ROLE ALL IN DATABASE d
 *                                             (setrole=0,     setdatabase=d.oid)
 *   4. ALTER ROLE ALL SET ...                 (setrole=0,     setdatabase=0)
 *   5. postgresql.conf / ALTER SYSTEM / built-in default
 *
 * For each application role and inspected setting, this query returns the
 * value the role would actually see on connect to the current database —
 * i.e., the most-specific row from pg_db_role_setting, falling back to
 * pg_settings.reset_val when no override at levels 1-4 applies.
 *
 * Caveat: pg_settings.reset_val reflects what the *connecting* session
 * would get on RESET, including any ALTER ROLE settings on the role
 * pgdoctor connects as. Run pgdoctor as a role with no overrides for the
 * inspected settings, otherwise the level-5 fallback can be wrong for
 * other roles. (Switching to pg_file_settings + boot_val to remove this
 * caveat is a planned follow-up; it requires the pg_read_all_settings
 * predefined role.)
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
  SELECT s.name, s.reset_val, s.unit
  FROM pg_settings AS s
  WHERE s.name IN (
    'statement_timeout'
    , 'idle_in_transaction_session_timeout'
    , 'transaction_timeout'
    , 'log_min_duration_statement'
  )
)

, current_db AS (
  SELECT d.oid FROM pg_database AS d WHERE d.datname = current_database()
)

-- Every pg_db_role_setting entry that could apply, parsed into key/value.
-- Filtered to our inspected settings so the join below stays tiny.
-- substring(... from position('=' in ...) + 1) handles values that contain '='.
, overrides AS (
  SELECT
    drs.setrole AS role_oid
    , drs.setdatabase AS db_oid
    , split_part(cfg, '=', 1) AS param_name
    , substring(cfg FROM position('=' IN cfg) + 1) AS param_value
  FROM pg_db_role_setting AS drs
  CROSS JOIN LATERAL unnest(coalesce(drs.setconfig, ARRAY[]::text [])) AS cfg
  WHERE split_part(cfg, '=', 1) IN (
    'statement_timeout'
    , 'idle_in_transaction_session_timeout'
    , 'transaction_timeout'
    , 'log_min_duration_statement'
  )
)

-- Cross every role with every setting; LEFT JOIN every candidate override.
-- Each (role, setting) row can produce up to 4 candidate rows; we pick the
-- most-specific one (lowest priority number) via DISTINCT ON below.
, candidates AS (
  SELECT
    r.rolname
    , s.name AS setting_name
    , s.reset_val AS system_default
    , s.unit
    , o.param_value AS override_value
    , CASE
      WHEN o.role_oid = r.oid AND o.db_oid = cdb.oid THEN 1
      WHEN o.role_oid = r.oid AND o.db_oid = 0 THEN 2
      WHEN o.role_oid = 0 AND o.db_oid = cdb.oid THEN 3
      WHEN o.role_oid = 0 AND o.db_oid = 0 THEN 4
    END AS priority
  FROM roles AS r
  CROSS JOIN settings AS s
  CROSS JOIN current_db AS cdb
  LEFT JOIN overrides AS o
    ON o.param_name = s.name
    AND (
      (o.role_oid = r.oid AND o.db_oid = cdb.oid)
      OR (o.role_oid = r.oid AND o.db_oid = 0)
      OR (o.role_oid = 0 AND o.db_oid = cdb.oid)
      OR (o.role_oid = 0 AND o.db_oid = 0)
    )
)

, winners AS (
  SELECT DISTINCT ON (rolname, setting_name)
    rolname
    , setting_name
    , system_default
    , unit
    , override_value
    , priority
  FROM candidates
  ORDER BY rolname, setting_name, priority NULLS LAST
)

SELECT
  rolname::varchar AS role_name
  , setting_name::varchar AS setting_name
  , system_default
  , unit
  , coalesce(override_value, system_default) AS setting_value
  , CASE priority
    WHEN 1 THEN 'OVERRIDE_ROLE_DB'
    WHEN 2 THEN 'OVERRIDE_ROLE'
    WHEN 3 THEN 'OVERRIDE_DATABASE'
    WHEN 4 THEN 'OVERRIDE_ALL'
    ELSE 'DEFAULT'
  END AS status
FROM winners
ORDER BY rolname, setting_name;
