-- name: DatabaseFreezeAge :one
-- XID and MultiXact freeze age for the connected database, plus the GUCs the
-- thresholds are derived from.
--
-- Only the connected database: the other rows of pg_database (template0,
-- template1, and whatever else lives in the cluster) cannot be vacuumed from
-- here, so reporting their age produces findings nobody can act on.
SELECT
  d.datname::text AS database_name
  , d.datfrozenxid::text AS frozen_xid
  , age(d.datfrozenxid)::bigint AS freeze_age
  , d.datminmxid::text AS min_multixact_id
  -- mxid_age('0'::xid) returns 2147483647. Guard it: a database that has never
  -- allocated a MultiXact would otherwise fabricate an instant FAIL.
  , CASE WHEN d.datminmxid <> '0'::xid THEN mxid_age(d.datminmxid)::bigint ELSE 0 END AS multixact_age
  , g.freeze_max_age
  , g.multixact_freeze_max_age
  , g.failsafe_age
  , g.multixact_failsafe_age
FROM pg_catalog.pg_database AS d
CROSS JOIN (
  -- One aggregate pass instead of a correlated subquery per GUC. All are raw
  -- integers in XID units, so setting::bigint needs no unit parsing. The failsafe
  -- GUCs are PG14+, so COALESCE to the documented default lets an older major
  -- degrade to the documented value instead of erroring.
  SELECT
    coalesce(max(CASE WHEN s.name = 'autovacuum_freeze_max_age' THEN s.setting::bigint END), 200000000) AS freeze_max_age
    , coalesce(
      max(CASE WHEN s.name = 'autovacuum_multixact_freeze_max_age' THEN s.setting::bigint END), 400000000
    ) AS multixact_freeze_max_age
    , coalesce(max(CASE WHEN s.name = 'vacuum_failsafe_age' THEN s.setting::bigint END), 1600000000) AS failsafe_age
    , coalesce(
      max(CASE WHEN s.name = 'vacuum_multixact_failsafe_age' THEN s.setting::bigint END), 1600000000
    ) AS multixact_failsafe_age
  FROM pg_catalog.pg_settings AS s
  WHERE s.name IN (
    'autovacuum_freeze_max_age'
    , 'autovacuum_multixact_freeze_max_age'
    , 'vacuum_failsafe_age'
    , 'vacuum_multixact_failsafe_age'
  )
) AS g
WHERE d.datname = current_database();

-- name: TableFreezeAge :many
-- XID and MultiXact freeze age per VACUUM target, for targets that have exceeded
-- their reporting floor, ranked by how many times over their anti-wraparound
-- trigger they are.
--
-- Rows are grouped by vacuum_target, not by relation: a TOAST relation is only
-- ever vacuumed through its parent, so listing the two separately produces a
-- pg_toast_NNNNN row the operator cannot identify and cannot act on. The group
-- carries the worst age of its members.
--
-- relkind IN ('r','m','t') is the exact set vac_update_datfrozenxid() counts;
-- 'p' has no storage. There is deliberately NO nspname filter: pg_catalog
-- relations expose an abandoned logical slot's catalog_xmin pin, and matviews and
-- orphaned pg_temp_* relations age independently of anything in public.
-- pg_stat_all_tables (not pg_stat_user_tables) because the latter excludes
-- pg_catalog and pg_toast, which would NULL out the vacuum history of most rows
-- returned here.
--
-- Size is a lock-free relpages estimate. pg_total_relation_size() takes an
-- AccessShareLock, and a new AccessShareLock request queues behind a *waiting*
-- AccessExclusiveLock, so it makes this check time out during exactly the DDL
-- pile-up it exists to diagnose. relpages is only refreshed by VACUUM/ANALYZE
-- and is 0 on a never-vacuumed relation, hence relpages is returned too so the
-- caller can render "unknown" rather than "0 B".
WITH settings AS (
  -- The failsafe GUCs are PG14+; COALESCE degrades to the documented default.
  SELECT
    coalesce(max(CASE WHEN s.name = 'autovacuum_freeze_max_age' THEN s.setting::bigint END), 200000000) AS freeze_max_age
    , coalesce(
      max(CASE WHEN s.name = 'autovacuum_multixact_freeze_max_age' THEN s.setting::bigint END), 400000000
    ) AS multixact_freeze_max_age
    , coalesce(max(CASE WHEN s.name = 'vacuum_failsafe_age' THEN s.setting::bigint END), 1600000000) AS failsafe_age
    , coalesce(
      max(CASE WHEN s.name = 'vacuum_multixact_failsafe_age' THEN s.setting::bigint END), 1600000000
    ) AS multixact_failsafe_age
  FROM pg_catalog.pg_settings AS s
  WHERE s.name IN (
    'autovacuum_freeze_max_age'
    , 'autovacuum_multixact_freeze_max_age'
    , 'vacuum_failsafe_age'
    , 'vacuum_multixact_failsafe_age'
  )
)

, relations AS (
  SELECT
    c.relkind
    , (n.nspname || '.' || c.relname)::text AS relation_name
    -- What the operator runs VACUUM (FREEZE) on. A TOAST relation resolves to its
    -- parent, which processes TOAST by default.
    , coalesce(p.oid, c.oid) AS target_oid
    , coalesce(pn.nspname || '.' || p.relname, n.nspname || '.' || c.relname)::text AS vacuum_target
    , age(c.relfrozenxid)::bigint AS freeze_age
    , CASE WHEN c.relminmxid <> '0'::xid THEN mxid_age(c.relminmxid)::bigint ELSE 0 END AS multixact_age
    , o.xid_reloption
    , o.multixact_reloption
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
  LEFT JOIN pg_catalog.pg_class AS p ON c.relkind = 't' AND p.reltoastrelid = c.oid
  LEFT JOIN pg_catalog.pg_namespace AS pn ON pn.oid = p.relnamespace
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
  SELECT r.*
  FROM relations AS r
  WHERE
    -- Reporting floor = the WARN threshold, so a healthy instance returns 0 rows.
    -- Ages just under the trigger are the top of the normal sawtooth: nothing
    -- vacuums a low-churn relation until the trigger fires, so peak age IS the
    -- trigger. Only exceeding it means autovacuum is losing.
    r.freeze_age >= 2 * r.effective_freeze_max_age
    OR r.multixact_age >= 2 * r.effective_multixact_freeze_max_age
)

, targets AS (
  SELECT
    a.target_oid
    , a.vacuum_target
    -- The group is as bad as its worst member, against its tightest trigger.
    , max(a.freeze_age) AS freeze_age
    , max(a.multixact_age) AS multixact_age
    , min(a.effective_freeze_max_age) AS effective_freeze_max_age
    , min(a.effective_multixact_freeze_max_age) AS effective_multixact_freeze_max_age
    , min(a.failsafe_age) AS failsafe_age
    , min(a.multixact_failsafe_age) AS multixact_failsafe_age
    , max(a.xid_reloption) AS xid_reloption
    , max(a.multixact_reloption) AS multixact_reloption
    , count(*) AS grouped_relations
    , count(*) FILTER (WHERE a.relkind = 't') AS toast_relations
    -- Which member is worst, so Debug can name the TOAST relation that pulled the
    -- group in without putting it in front of the operator.
    , (array_agg(a.relation_name ORDER BY a.freeze_age DESC))[1]::text AS worst_relation
    -- Counts groups, not relations: window functions run after GROUP BY.
    , count(*) OVER () AS total_above_floor
  FROM above_floor AS a
  GROUP BY a.target_oid, a.vacuum_target
  ORDER BY
    greatest(
      max(a.freeze_age)::numeric / nullif(min(a.effective_freeze_max_age), 0)
      , max(a.multixact_age)::numeric / nullif(min(a.effective_multixact_freeze_max_age), 0)
    ) DESC
  LIMIT 50
)

SELECT
  t.vacuum_target
  , t.worst_relation
  , t.grouped_relations
  , t.toast_relations
  , c.relkind::text AS relkind
  , c.relpages::bigint AS relpages
  , (c.relpages + coalesce(toast.relpages, 0) + coalesce(idx.index_pages, 0))::bigint
    * current_setting('block_size')::bigint AS size_bytes_est
  , t.freeze_age
  , t.multixact_age
  , t.effective_freeze_max_age
  , t.effective_multixact_freeze_max_age
  , t.failsafe_age
  , t.multixact_failsafe_age
  , t.xid_reloption
  , t.multixact_reloption
  , s.last_autovacuum
  , s.last_vacuum
  , coalesce(s.autovacuum_count, 0) AS autovacuum_count
  , coalesce(s.vacuum_count, 0) AS vacuum_count
  , t.total_above_floor
FROM targets AS t
-- Sizes and vacuum history are joined after the LIMIT, for 50 rows rather than
-- every relation in the database.
INNER JOIN pg_catalog.pg_class AS c ON c.oid = t.target_oid
LEFT JOIN pg_catalog.pg_class AS toast ON toast.oid = c.reltoastrelid
LEFT JOIN pg_catalog.pg_stat_all_tables AS s ON s.relid = t.target_oid
LEFT JOIN LATERAL (
  SELECT sum(ic.relpages)::bigint AS index_pages
  FROM pg_catalog.pg_index AS i
  INNER JOIN pg_catalog.pg_class AS ic ON ic.oid = i.indexrelid
  WHERE i.indrelid IN (c.oid, c.reltoastrelid)
) AS idx ON TRUE
ORDER BY
  greatest(
    t.freeze_age::numeric / nullif(t.effective_freeze_max_age, 0)
    , t.multixact_age::numeric / nullif(t.effective_multixact_freeze_max_age, 0)
  ) DESC;
