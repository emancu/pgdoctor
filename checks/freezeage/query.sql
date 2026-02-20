-- name: DatabaseFreezeAge :many
-- Gets transaction ID age for all databases.
SELECT
  datname::text AS database_name
  , datfrozenxid::text AS frozen_xid
  , age(datfrozenxid) AS freeze_age
  , (
    SELECT s.setting::bigint FROM pg_settings AS s
    WHERE s.name = 'autovacuum_freeze_max_age'
  ) AS freeze_max_age
FROM pg_database
WHERE datallowconn = true
ORDER BY age(datfrozenxid) DESC;

-- name: TableFreezeAge :many
-- Gets transaction ID age for tables with oldest frozen XIDs.
SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , c.relfrozenxid::text AS frozen_xid
  , s.last_autovacuum
  , s.last_vacuum
  , s.autovacuum_count
  , s.vacuum_count
  , age(c.relfrozenxid) AS freeze_age
  , pg_total_relation_size(c.oid) AS table_size_bytes
FROM pg_class AS c
INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
LEFT JOIN pg_stat_user_tables AS s ON c.oid = s.relid
WHERE
  c.relkind = 'r'
  AND n.nspname = 'public'
  AND c.relfrozenxid != '0'
ORDER BY age(c.relfrozenxid) DESC
LIMIT 50;
