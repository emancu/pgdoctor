-- name: DatabaseFreezeAge :one
-- Connected database only: the other pg_database rows cannot be vacuumed from
-- this connection, so their age is not actionable here.
SELECT
  d.datname::text AS database_name
  , d.datfrozenxid::text AS frozen_xid
  , age(d.datfrozenxid)::bigint AS freeze_age
  , d.datminmxid::text AS min_multixact_id
  -- mxid_age('0'::xid) returns 2147483647, which would fabricate an instant FAIL.
  , CASE WHEN d.datminmxid <> '0'::xid THEN mxid_age(d.datminmxid)::bigint ELSE 0 END AS multixact_age
  , g.freeze_max_age
  , g.multixact_freeze_max_age
  , g.failsafe_age
  , g.multixact_failsafe_age
FROM pg_catalog.pg_database AS d
CROSS JOIN (
  -- The failsafe GUCs are PG14+; COALESCE degrades to the documented default
  -- instead of erroring on an older major.
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

-- name: HorizonPins :many
-- Durable pins on the xmin horizon: replication slots and prepared transactions
-- are on-disk state that does not resolve itself, so a single read is a snapshot
-- rather than a race. Backends, lock waiters and in-flight vacuums are
-- deliberately absent — reading those needs luck in timing and belongs to a live
-- investigation (`houston dba xmin`).
--
-- Slot recency is the age of the pinned xid, not wall-clock time: the column PG17
-- added for that does not exist on PG14, which is this check's floor.
WITH pins AS (
  SELECT
    (CASE WHEN s.slot_type = 'logical' THEN 'logical_slot' ELSE 'physical_slot' END)::text AS source
    , s.slot_name::text AS object_name
    , v.pin_column
    , v.pinned_xid
    , coalesce(s.active, FALSE) AS active
    , coalesce(s.wal_status, 'unknown')::text AS wal_status
    , format(
      '%s slot %s, %s, WAL %s'
      , s.slot_type
      , s.slot_name
      , CASE WHEN s.active THEN 'active' ELSE 'inactive' END
      , coalesce(s.wal_status, 'unknown')
    )::text AS detail
  FROM pg_catalog.pg_replication_slots AS s
  -- The two values pin independently: a logical slot usually holds only
  -- catalog_xmin, a physical slot with hot_standby_feedback only xmin.
  CROSS JOIN LATERAL (
    VALUES ('slot_xmin'::text, s.xmin), ('slot_catalog_xmin'::text, s.catalog_xmin)
  ) AS v (pin_column, pinned_xid)
  -- A slot that pins nothing has a NULL here; age(NULL) is NULL, but dropping the
  -- row keeps a non-pinning slot out of the result set entirely.
  WHERE v.pinned_xid IS NOT NULL AND v.pinned_xid <> '0'::xid

  UNION ALL

  -- A prepared transaction is always holding, so there is no inactive variant to
  -- reason about and the wal_status rules stay slot-only.
  SELECT
    'prepared_xact'::text
    , x.gid::text
    , 'prepared_xid'::text
    , x.transaction
    , TRUE
    , 'unknown'::text
    , format(
      'prepared transaction %s on %s, owner %s, prepared %s ago'
      , x.gid, x.database, x.owner, date_trunc('second', now() - x.prepared)
    )::text
  FROM pg_catalog.pg_prepared_xacts AS x
  WHERE x.transaction <> '0'::xid
)

SELECT
  p.source
  , p.object_name
  , p.pin_column
  , age(p.pinned_xid)::bigint AS pin_age
  , p.active
  , p.wal_status
  , p.detail
FROM pins AS p
ORDER BY pin_age DESC;

-- name: TableFreezeAge :many
-- Freeze age per VACUUM target, grouped by target rather than relation because a
-- TOAST relation is only ever vacuumed through its parent.
--
-- relkind IN ('r','m','t') is the exact set vac_update_datfrozenxid() counts. No
-- nspname filter, so pg_stat_all_tables is required: pg_stat_user_tables excludes
-- pg_catalog and pg_toast and would NULL out most vacuum history here.
--
-- Size avoids pg_total_relation_size(), whose AccessShareLock queues behind a
-- *waiting* AccessExclusiveLock and would time this check out during the DDL
-- pile-up it exists to diagnose. relpages is returned so relpages = 0 (never
-- vacuumed) renders as "unknown" rather than "0 B".
WITH settings AS (
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
    , coalesce(p.oid, c.oid) AS target_oid
    , coalesce(pn.nspname || '.' || p.relname, n.nspname || '.' || c.relname)::text AS vacuum_target
    , age(c.relfrozenxid)::bigint AS freeze_age
    , CASE WHEN c.relminmxid <> '0'::xid THEN mxid_age(c.relminmxid)::bigint ELSE 0 END AS multixact_age
    , o.xid_reloption
    , o.multixact_reloption
    , g.failsafe_age
    , g.multixact_failsafe_age
    -- A reloption can only LOWER the trigger, so the GUC is the upper bound.
    , least(coalesce(nullif(o.xid_reloption, 0), g.freeze_max_age), g.freeze_max_age) AS effective_freeze_max_age
    , least(coalesce(nullif(o.multixact_reloption, 0), g.multixact_freeze_max_age), g.multixact_freeze_max_age)
      AS effective_multixact_freeze_max_age
  FROM pg_catalog.pg_class AS c
  INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
  CROSS JOIN settings AS g
  LEFT JOIN pg_catalog.pg_class AS p ON c.relkind = 't' AND p.reltoastrelid = c.oid
  LEFT JOIN pg_catalog.pg_namespace AS pn ON pn.oid = p.relnamespace
  -- 0 means "no override": the reloption minimums are 100000 (XID) and 10000
  -- (MultiXact), so 0 cannot collide with a real value.
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
    r.*
    -- Per-member severity, so the group can report an age and the trigger it was
    -- measured against from the SAME member (see targets below).
    , r.freeze_age::numeric / nullif(r.effective_freeze_max_age, 0) AS xid_ratio
    , r.multixact_age::numeric / nullif(r.effective_multixact_freeze_max_age, 0) AS multixact_ratio
  FROM relations AS r
  WHERE
    -- Floor = the LOWER of the WARN and FAIL thresholds Go will apply, never just
    -- WARN. Go clamps WARN to the age() ceiling and caps FAIL at the failsafe, so
    -- a high trigger can make FAIL < WARN: at a 1.2B trigger, WARN clamps to
    -- 2147483647 and FAIL is 1.6B. Flooring at a raw 2 * trigger (2.4B) would be
    -- unreachable and would silently discard a 1.7B relation Go would FAIL.
    -- 4 * trigger >= 2 * trigger always, so least(2 * trigger, failsafe, ceiling)
    -- is exactly min(clamped WARN, FAIL).
    r.freeze_age >= least(2 * r.effective_freeze_max_age, r.failsafe_age, 2147483647)
    OR r.multixact_age >= least(
      2 * r.effective_multixact_freeze_max_age, r.multixact_failsafe_age, 2147483647
    )
)

-- The worst member per counter, picked whole. Reporting max(age) against
-- min(trigger) as independent aggregates would pair one member's age with
-- another's trigger and fabricate a severity no relation has: a 390M/100M parent
-- and an 800M/400M TOAST (both WARN) would combine into 800M against 100M, a
-- FAIL. DISTINCT ON keeps age, trigger and reloption from the same row — and
-- keeps them NOT NULL, which an array_agg subscript would not.
, worst_xid AS (
  SELECT DISTINCT ON (a.target_oid)
    a.target_oid
    , a.freeze_age
    , a.effective_freeze_max_age
    , a.xid_reloption
    , a.failsafe_age
    , coalesce(a.xid_ratio, 0) AS xid_ratio
  FROM above_floor AS a
  ORDER BY a.target_oid, a.xid_ratio DESC NULLS LAST
)

, worst_multixact AS (
  SELECT DISTINCT ON (a.target_oid)
    a.target_oid
    , a.multixact_age
    , a.effective_multixact_freeze_max_age
    , a.multixact_reloption
    , a.multixact_failsafe_age
    , coalesce(a.multixact_ratio, 0) AS multixact_ratio
  FROM above_floor AS a
  ORDER BY a.target_oid, a.multixact_ratio DESC NULLS LAST
)

, grouped AS (
  SELECT
    a.target_oid
    , a.vacuum_target
    , count(*) AS grouped_relations
    , count(*) FILTER (WHERE a.relkind = 't') AS toast_relations
    -- For Debug: names the relation that pulled the group in, on either counter.
    , (array_agg(
      a.relation_name
      ORDER BY greatest(coalesce(a.xid_ratio, 0), coalesce(a.multixact_ratio, 0)) DESC
    ))[1]::text AS worst_relation
  FROM above_floor AS a
  GROUP BY a.target_oid, a.vacuum_target
)

, targets AS (
  SELECT
    g.target_oid
    , g.vacuum_target
    , x.freeze_age
    , x.effective_freeze_max_age
    , x.xid_reloption
    , x.failsafe_age
    , m.multixact_age
    , m.effective_multixact_freeze_max_age
    , m.multixact_reloption
    , m.multixact_failsafe_age
    , g.grouped_relations
    , g.toast_relations
    , g.worst_relation
    -- Counts groups, not relations.
    , count(*) OVER () AS total_above_floor
  FROM grouped AS g
  INNER JOIN worst_xid AS x ON x.target_oid = g.target_oid
  INNER JOIN worst_multixact AS m ON m.target_oid = g.target_oid
  ORDER BY greatest(x.xid_ratio, m.multixact_ratio) DESC
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
-- Joined after the LIMIT: 50 lookups, not one per relation in the database.
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
