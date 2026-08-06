-- name: DatabaseFreezeAge :many
-- Gets XID and MultiXact freeze age for EVERY database in the cluster.
-- No datallowconn filter on purpose: the cluster-wide XID limit is
-- min(datfrozenxid) over every row of pg_database (vac_truncate_clog() does not
-- filter on datallowconn), so template0 and rdsadmin count towards the limit and
-- must be reported. datallowconn is returned so the check can label rows that a
-- plain VACUUM cannot fix.
SELECT
  d.datname::text AS database_name
  , d.datallowconn
  , (d.datname = current_database()) AS is_current_database
  , d.datfrozenxid::text AS frozen_xid
  , age(d.datfrozenxid)::bigint AS freeze_age
  , d.datminmxid::text AS min_multixact_id
  -- mxid_age('0'::xid) returns 2147483647. Guard it: a database that has never
  -- allocated a MultiXact would otherwise fabricate an instant FAIL.
  , CASE WHEN d.datminmxid <> '0'::xid THEN mxid_age(d.datminmxid)::bigint ELSE 0 END AS multixact_age
  , g.freeze_max_age
  , g.multixact_freeze_max_age
  , g.freeze_table_age
  , g.multixact_freeze_table_age
  , g.freeze_min_age
  , g.failsafe_age
  , g.multixact_failsafe_age
FROM pg_catalog.pg_database AS d
CROSS JOIN (
  -- Single aggregate pass over pg_settings instead of one correlated subquery
  -- per GUC. All of these are raw integers in XID units, so setting::bigint is
  -- safe (no unit suffix to parse). COALESCE to the documented default keeps the
  -- columns NOT NULL on a major that lacks one of the GUCs.
  SELECT
    coalesce(max(CASE WHEN s.name = 'autovacuum_freeze_max_age' THEN s.setting::bigint END), 200000000) AS freeze_max_age
    , coalesce(
      max(CASE WHEN s.name = 'autovacuum_multixact_freeze_max_age' THEN s.setting::bigint END), 400000000
    ) AS multixact_freeze_max_age
    , coalesce(max(CASE WHEN s.name = 'vacuum_freeze_table_age' THEN s.setting::bigint END), 150000000) AS freeze_table_age
    , coalesce(
      max(CASE WHEN s.name = 'vacuum_multixact_freeze_table_age' THEN s.setting::bigint END), 150000000
    ) AS multixact_freeze_table_age
    , coalesce(max(CASE WHEN s.name = 'vacuum_freeze_min_age' THEN s.setting::bigint END), 50000000) AS freeze_min_age
    , coalesce(max(CASE WHEN s.name = 'vacuum_failsafe_age' THEN s.setting::bigint END), 1600000000) AS failsafe_age
    , coalesce(
      max(CASE WHEN s.name = 'vacuum_multixact_failsafe_age' THEN s.setting::bigint END), 1600000000
    ) AS multixact_failsafe_age
  FROM pg_catalog.pg_settings AS s
  WHERE s.name IN (
    'autovacuum_freeze_max_age'
    , 'autovacuum_multixact_freeze_max_age'
    , 'vacuum_freeze_table_age'
    , 'vacuum_multixact_freeze_table_age'
    , 'vacuum_freeze_min_age'
    , 'vacuum_failsafe_age'
    , 'vacuum_multixact_failsafe_age'
  )
) AS g
ORDER BY age(d.datfrozenxid) DESC;

-- name: TableFreezeAge :many
-- Gets XID and MultiXact freeze age per relation, for relations that have
-- already reached their WARN threshold (the reporting floor), ranked by the
-- fraction of their anti-wraparound trigger consumed.
--
-- relkind IN ('r','m','t') is the exact set vac_update_datfrozenxid() counts;
-- 'p' has no storage. There is deliberately NO nspname filter: pg_catalog
-- relations expose an abandoned logical slot's catalog_xmin pin, and TOAST /
-- matview / orphaned pg_temp_* relations age independently of their parents.
-- pg_stat_all_tables (not pg_stat_user_tables) because the latter is defined as
-- pg_stat_all_tables WHERE schemaname NOT IN ('pg_catalog','information_schema')
-- AND schemaname !~ '^pg_toast', which would NULL out vacuum history for every
-- relation the two changes above added.
--
-- Size is a lock-free relpages estimate. pg_total_relation_size() takes an
-- AccessShareLock, and a new AccessShareLock request queues behind a *waiting*
-- AccessExclusiveLock, so it makes this check time out during exactly the DDL
-- pile-up it exists to diagnose. relpages is only refreshed by VACUUM/ANALYZE
-- and is 0 on a never-vacuumed relation, hence relpages is returned too so the
-- caller can render "unknown" rather than "0 B".
WITH settings AS (
  SELECT
    coalesce(max(CASE WHEN s.name = 'autovacuum_freeze_max_age' THEN s.setting::bigint END), 200000000) AS freeze_max_age
    , coalesce(
      max(CASE WHEN s.name = 'autovacuum_multixact_freeze_max_age' THEN s.setting::bigint END), 400000000
    ) AS multixact_freeze_max_age
    , coalesce(max(CASE WHEN s.name = 'vacuum_freeze_table_age' THEN s.setting::bigint END), 150000000) AS freeze_table_age
    , coalesce(
      max(CASE WHEN s.name = 'vacuum_multixact_freeze_table_age' THEN s.setting::bigint END), 150000000
    ) AS multixact_freeze_table_age
    , coalesce(max(CASE WHEN s.name = 'vacuum_failsafe_age' THEN s.setting::bigint END), 1600000000) AS failsafe_age
    , coalesce(
      max(CASE WHEN s.name = 'vacuum_multixact_failsafe_age' THEN s.setting::bigint END), 1600000000
    ) AS multixact_failsafe_age
  FROM pg_catalog.pg_settings AS s
  WHERE s.name IN (
    'autovacuum_freeze_max_age'
    , 'autovacuum_multixact_freeze_max_age'
    , 'vacuum_freeze_table_age'
    , 'vacuum_multixact_freeze_table_age'
    , 'vacuum_failsafe_age'
    , 'vacuum_multixact_failsafe_age'
  )
)

, relations AS (
  SELECT
    c.oid AS reloid
    , c.reltoastrelid
    , c.relkind
    , c.relname
    , n.nspname
    , c.relpages
    , c.relfrozenxid
    , c.relminmxid
    , age(c.relfrozenxid)::bigint AS freeze_age
    , CASE WHEN c.relminmxid <> '0'::xid THEN mxid_age(c.relminmxid)::bigint ELSE 0 END AS multixact_age
    , o.xid_reloption
    , o.multixact_reloption
    , g.freeze_table_age
    , g.multixact_freeze_table_age
    , g.failsafe_age
    , g.multixact_failsafe_age
    -- A per-table reloption can only LOWER the trigger, never raise it, so the
    -- GUC is always the upper bound.
    , least(coalesce(nullif(o.xid_reloption, 0), g.freeze_max_age), g.freeze_max_age) AS effective_freeze_max_age
    , least(coalesce(nullif(o.multixact_reloption, 0), g.multixact_freeze_max_age), g.multixact_freeze_max_age)
      AS effective_multixact_freeze_max_age
  FROM pg_catalog.pg_class AS c
  INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
  CROSS JOIN settings AS g
  -- 0 means "no per-table override": the reloption minimum is 100000, so 0 is
  -- unambiguous and keeps the column NOT NULL.
  CROSS JOIN LATERAL (
    SELECT
      coalesce(
        substring(array_to_string(c.reloptions, ',') FROM 'autovacuum_freeze_max_age=(\d+)')::bigint, 0
      ) AS xid_reloption
      , coalesce(
        substring(array_to_string(c.reloptions, ',') FROM 'autovacuum_multixact_freeze_max_age=(\d+)')::bigint, 0
      ) AS multixact_reloption
  ) AS o
  WHERE
    c.relkind IN ('r', 'm', 't')
    AND c.relfrozenxid <> '0'::xid
)

, above_floor AS (
  SELECT
    r.reloid
    , r.reltoastrelid
    , r.relkind
    , r.relname
    , r.nspname
    , r.relpages
    , r.relfrozenxid
    , r.relminmxid
    , r.freeze_age
    , r.multixact_age
    , r.xid_reloption
    , r.multixact_reloption
    , r.freeze_table_age
    , r.multixact_freeze_table_age
    , r.failsafe_age
    , r.multixact_failsafe_age
    , r.effective_freeze_max_age
    , r.effective_multixact_freeze_max_age
    -- Total above the floor, so a truncated list can say "1,847 relations above
    -- floor, worst 50 shown". Free: the ORDER BY ... LIMIT already sorts them all.
    , count(*) OVER () AS total_above_floor
  FROM relations AS r
  WHERE
    -- Reporting floor = the WARN threshold on either counter, so healthy
    -- instances return 0 rows. Both arms are PostgreSQL's own aggressive-scan
    -- point for that counter; they must stay in step with the Go thresholds or
    -- relations get filtered out before Go can classify them.
    r.freeze_age >= least(r.freeze_table_age, (0.95 * r.effective_freeze_max_age)::bigint)
    OR r.multixact_age >= least(
      r.multixact_freeze_table_age, (0.95 * r.effective_multixact_freeze_max_age)::bigint
    )
  ORDER BY
    greatest(
      r.freeze_age::numeric / nullif(r.effective_freeze_max_age, 0)
      , r.multixact_age::numeric / nullif(r.effective_multixact_freeze_max_age, 0)
    ) DESC
  LIMIT 50
)

SELECT
  (a.nspname || '.' || a.relname)::text AS table_name
  -- What the operator actually runs VACUUM (FREEZE) on: a TOAST relation is
  -- vacuumed through its parent, which processes TOAST by default.
  , coalesce(parent.parent_name, (a.nspname || '.' || a.relname)::text) AS vacuum_target
  , a.relkind::text AS relkind
  , a.relpages::bigint AS relpages
  , (a.relpages + coalesce(toast.relpages, 0) + coalesce(idx.index_pages, 0))::bigint
    * current_setting('block_size')::bigint AS size_bytes_est
  , a.relfrozenxid::text AS frozen_xid
  , a.freeze_age
  , a.relminmxid::text AS min_multixact_id
  , a.multixact_age
  , a.effective_freeze_max_age
  , a.effective_multixact_freeze_max_age
  , a.freeze_table_age
  , a.multixact_freeze_table_age
  , a.failsafe_age
  , a.multixact_failsafe_age
  , a.xid_reloption
  , a.multixact_reloption
  , s.last_autovacuum
  , s.last_vacuum
  , coalesce(s.autovacuum_count, 0) AS autovacuum_count
  , coalesce(s.vacuum_count, 0) AS vacuum_count
  , a.total_above_floor
FROM above_floor AS a
LEFT JOIN pg_catalog.pg_class AS toast ON toast.oid = a.reltoastrelid
LEFT JOIN pg_catalog.pg_stat_all_tables AS s ON s.relid = a.reloid
LEFT JOIN LATERAL (
  SELECT (pn.nspname || '.' || p.relname)::text AS parent_name
  FROM pg_catalog.pg_class AS p
  INNER JOIN pg_catalog.pg_namespace AS pn ON pn.oid = p.relnamespace
  WHERE a.relkind = 't' AND p.reltoastrelid = a.reloid
) AS parent ON TRUE
LEFT JOIN LATERAL (
  SELECT sum(ic.relpages)::bigint AS index_pages
  FROM pg_catalog.pg_index AS i
  INNER JOIN pg_catalog.pg_class AS ic ON ic.oid = i.indexrelid
  WHERE i.indrelid IN (a.reloid, a.reltoastrelid)
) AS idx ON TRUE
ORDER BY
  greatest(
    a.freeze_age::numeric / nullif(a.effective_freeze_max_age, 0)
    , a.multixact_age::numeric / nullif(a.effective_multixact_freeze_max_age, 0)
  ) DESC;

-- name: XminHorizonBlockers :many
-- Every live object that can pin the xmin horizon and stop vacuum from advancing
-- relfrozenxid, normalized into one ranked list: backends, walsenders,
-- autovacuum workers, replication slots and prepared transactions.
--
-- A slot legitimately emits TWO rows when both xmin and catalog_xmin are set:
-- they pin different horizons (data vs catalog), it is not duplication.
-- xid has no ordering operator and would break at wraparound, so ages are always
-- compared via age(), never with <. Works unchanged on PG 15-18 and on a standby.
SELECT
  b.source
  , b.object
  , b.pin_kind
  , b.pinned_xid::text AS pinned_xid
  , age(b.pinned_xid)::bigint AS pinned_xid_age
  , b.horizon_scope
  , b.duration_seconds
  , b.duration_estimated
  , b.privilege_masked
  , b.inactive
  , b.details
  -- At the RDS default of -1 a slot never self-invalidates, which is what makes
  -- an inactive slot's pin permanent.
  , current_setting('max_slot_wal_keep_size')::text AS max_slot_wal_keep_size
FROM (
  SELECT
    CASE a.backend_type
      WHEN 'walsender' THEN 'standby_feedback'
      WHEN 'autovacuum worker' THEN 'autovacuum'
      ELSE 'backend'
    END::text AS source
    , a.pid::text AS object
    , p.pin_kind
    , p.pinned_xid
    , 'data+catalog'::text AS horizon_scope
    , floor(extract(EPOCH FROM (now() - coalesce(a.xact_start, a.backend_start))))::bigint AS duration_seconds
    -- xact_start NULL (masked, or no transaction yet) means duration was measured
    -- from backend_start and overstates the pin's age.
    , (a.xact_start IS NULL) AS duration_estimated
    -- Without pg_read_all_stats, query masks to '<insufficient privilege>' and
    -- state/backend_type/xact_start go NULL while pid and backend_xmin stay visible.
    , (a.query = '<insufficient privilege>' OR a.state IS NULL) AS privilege_masked
    -- Only meaningful for slots: nothing will ever advance an inactive slot's pin.
    , FALSE AS inactive
    , concat_ws(
      ' ', a.datname, a.usename, nullif(a.application_name, '')
      , '[' || a.state || ']', left(regexp_replace(a.query, '\s+', ' ', 'g'), 120)
    ) AS details
  FROM pg_catalog.pg_stat_activity AS a
  CROSS JOIN LATERAL (
    SELECT v.pinned_xid, v.pin_kind
    FROM (VALUES (a.backend_xid, 'backend_xid'::text), (a.backend_xmin, 'backend_xmin'::text)) AS v (pinned_xid, pin_kind)
    WHERE v.pinned_xid IS NOT NULL
    ORDER BY age(v.pinned_xid) DESC
    LIMIT 1
  ) AS p
  WHERE a.pid <> pg_backend_pid()

  UNION ALL

  SELECT
    CASE WHEN s.slot_type = 'logical' THEN 'logical_slot' ELSE 'physical_slot' END
    , s.slot_name::text
    , k.pin_kind
    , k.pinned_xid
    , CASE k.pin_kind WHEN 'slot_catalog_xmin' THEN 'catalog' ELSE 'data+catalog' END
    , NULL::bigint
    , FALSE
    , FALSE
    , NOT s.active
    , concat_ws(
      ' ', s.database, s.plugin
      , CASE WHEN s.active THEN 'active pid=' || s.active_pid ELSE 'INACTIVE' END
      , 'wal_status=' || s.wal_status
    )
  FROM pg_catalog.pg_replication_slots AS s
  CROSS JOIN LATERAL (VALUES (s.xmin, 'slot_xmin'::text), (s.catalog_xmin, 'slot_catalog_xmin'::text)) AS k (pinned_xid, pin_kind)
  WHERE k.pinned_xid IS NOT NULL

  UNION ALL

  SELECT
    'prepared_xact'
    , x.gid::text
    , 'prepared_xid'
    , x.transaction
    , 'data+catalog'
    , floor(extract(EPOCH FROM (now() - x.prepared)))::bigint
    , FALSE
    , FALSE
    , FALSE
    , concat_ws(' ', x.database, x.owner::text)
  FROM pg_catalog.pg_prepared_xacts AS x
) AS b
ORDER BY pinned_xid_age DESC, b.source
LIMIT 20;
