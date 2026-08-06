# Freeze Age Check

Monitors how much headroom is left before PostgreSQL's two 32-bit wraparound counters — transaction IDs and MultiXacts — force an anti-wraparound vacuum, and reports what is stopping vacuum from reclaiming that headroom.

## Background

PostgreSQL uses 32-bit transaction IDs (XIDs). Rows carry the XID that created them, and vacuum must periodically "freeze" old rows so the counter can be reused. Two things make this a production concern rather than a background detail:

1. **Vacuum can only freeze rows older than the xmin horizon.** Any long transaction, replication slot `xmin`/`catalog_xmin`, standby feedback or prepared transaction holds that horizon back. Vacuum then runs, finishes successfully, and advances nothing.
2. **The counter never stops.** A single PL/pgSQL loop was measured consuming 80,001 XIDs in 0.4 seconds. At that rate one background job burns 200M XIDs in under 20 minutes.

MultiXacts are a second, independent counter with its own trigger, its own age and its own wall. A MultiXact is allocated when a row needs a **second** lock holder — the first one fits in `xmax`. Concurrent child inserts taking `FOR KEY SHARE` on one hot parent row are the classic generator. **RDS exposes no multixact CloudWatch metric at all**, so this clock is invisible to normal alarms; `MaximumUsedTransactionIDs` only tracks XIDs.

## Thresholds

Nothing here is a number we invented. Every threshold is derived from the GUCs that change PostgreSQL's own behaviour, per relation and per database:

| | WARN | FAIL | At stock GUCs |
|---|---|---|---|
| XID | `min(vacuum_freeze_table_age, 0.95 × effective autovacuum_freeze_max_age)` | `min(4 × effective trigger, vacuum_failsafe_age)` | 150M / 800M |
| MultiXact | `min(vacuum_multixact_freeze_table_age, 0.95 × effective autovacuum_multixact_freeze_max_age)` | `min(4 × effective trigger, vacuum_multixact_failsafe_age)` | 150M / 1.6B |

The two rows are the **same formula**. PostgreSQL gives the MultiXact counter a complete, independent set of GUCs — its own trigger (`autovacuum_multixact_freeze_max_age`), its own aggressive-scan point (`vacuum_multixact_freeze_table_age`) and its own failsafe (`vacuum_multixact_failsafe_age`) — so the derivation is identical and only the inputs differ. At stock settings both counters therefore WARN at 150M, even though the MultiXact trigger sits at 400M rather than 200M.

**Why WARN there:** `min(vacuum_[multixact_]freeze_table_age, 0.95 × trigger)` is PostgreSQL's own formula for when it starts an *aggressive* (whole-relation) scan. The check fires at the point the database changes behaviour, not at a number that looked round. Healthy relations oscillate between `vacuum_freeze_min_age` (50M) and this value; a relation that stays above it is not oscillating any more.

**Why FAIL there:** the failsafe age (1.6B for both counters at defaults) is the real cliff — cost delay disabled and **index vacuuming skipped entirely**. 2B is not a usable design anchor: the documented recovery from the hard stop is single-user mode, which **does not exist on RDS**. `vacuum_failsafe_age` and `vacuum_multixact_failsafe_age` are read separately, so tuning one does not silently move the other counter's threshold.

`age()` and `mxid_age()` saturate at 2147483647, so no threshold above that is meaningful and all thresholds are clamped to it.

**Per-table reloptions can only lower the trigger, never raise it.** `ALTER TABLE ... SET (autovacuum_freeze_max_age = N)` is applied as `least(coalesce(N, GUC), GUC)`, matching PostgreSQL, and the effective trigger is what the percentages are measured against — the `% of Trigger` column shows it explicitly.

## Reporting floor

`table-freeze-age` only returns relations already at or above their WARN threshold. A healthy instance returns zero rows instead of shipping every relation across the wire. When more than 50 relations are above the floor (pg_partman partitions age in lockstep), the count is reported alongside the worst 50: *"1,847 relation(s) above the reporting floor, worst 50 shown"*.

## What It Checks

### database-freeze-age

Per-database `age(datfrozenxid)` against the effective trigger.

**Severity:** WARN / FAIL per the table above.

This check covers **every** row of `pg_database`, including ones that reject connections. The cluster-wide XID limit is `min(datfrozenxid)` over every row — `vac_truncate_clog()` does not filter on `datallowconn`. `template0` has `datallowconn = false` and RDS adds `rdsadmin`; excluding them lets a check PASS while the RDS `MaximumUsedTransactionIDs` alarm fires. Those rows are labelled **not fixable with a plain `VACUUM`** and never hidden:

```sql
-- template0: temporarily allow connections
ALTER DATABASE template0 ALLOW_CONNECTIONS true;
-- then, connected to template0:
VACUUM FREEZE;
ALTER DATABASE template0 ALLOW_CONNECTIONS false;
```

`rdsadmin` is not reachable by customers: open an AWS support case.

### table-freeze-age

Per-relation `age(relfrozenxid)`, **headline is headroom** — `effective_trigger - age(relfrozenxid)` XIDs remaining — plus the percentage of the trigger consumed, ranked by that percentage, with `Size (est)` as blast radius:

```
public.bookings (412.0GiB) is 31.0M XIDs from its anti-wraparound trigger (84% consumed)
```

Headroom is the headline because "how long do I have" is the number an operator acts on, and because a verdict would be dishonest: see [PASS semantics](#what-a-pass-actually-means).

Scope is `relkind IN ('r','m','t')` — the exact set `vac_update_datfrozenxid()` counts. `'p'` is excluded (no storage). There is **no `public` schema filter**, deliberately breaking the repo-wide convention: an abandoned logical replication slot pins `catalog_xmin` on `pg_catalog` relations, and TOAST relations, materialized views and orphaned `pg_temp_*` relations age independently of anything in `public`.

For a TOAST relation the `Vacuum Target` column is the **parent** table, because that is what the operator runs:

```sql
VACUUM (FREEZE, VERBOSE) public.bookings;  -- processes its TOAST relation by default
```

### database-multixact-age

Same shape against `pg_database.datminmxid`, derived from `autovacuum_multixact_freeze_max_age`, `vacuum_multixact_freeze_table_age` and `vacuum_multixact_failsafe_age` — WARN at 150M, FAIL at 1.6B on stock settings.

`mxid_age('0'::xid)` returns 2147483647, so the query guards with `datminmxid <> '0'::xid`. Without that guard a database that has never allocated a MultiXact would report an instant FAIL.

### table-multixact-age

Same, against `pg_class.relminmxid`, with the same `<> '0'::xid` guard. A per-table `autovacuum_multixact_freeze_max_age` reloption lowers the effective MultiXact trigger exactly as its XID counterpart does.

### horizon-blockers

Every live object that can pin the xmin horizon — backends holding `backend_xid`/`backend_xmin`, walsenders relaying standby feedback, autovacuum workers, replication slot `xmin`/`catalog_xmin`, and prepared transactions — normalized into one ranked list.

**Severity:**
- INFO baseline, always. Pins exist on every busy database; listing them is triage context, not an alarm.
- WARN at pin age >= `max(vacuum_freeze_min_age, 0.25 × effective trigger)` (50M at defaults). Below `vacuum_freeze_min_age` a pin cannot block any freezing vacuum would have done anyway. At ~1,000 XID/s a pin needs ~14 hours to reach 50M, so this gate is a duration filter by construction — a 5-minute transaction correctly never qualifies.
- FAIL at pin age >= the effective trigger.
- FAIL **at any age** for an inactive slot holding `xmin`/`catalog_xmin`: nothing will ever advance it, so the pin is monotonic by construction. At the RDS default `max_slot_wal_keep_size = -1` the slot never self-invalidates either, which is what makes the pin permanent (see `checks/replicationlag/README.md`).
- Capped at WARN for `standby_feedback` (the fix is on the replica, unactionable from the primary) and for `CREATE INDEX CONCURRENTLY` / `REINDEX CONCURRENTLY` (cancelling leaves an `INVALID` index behind — do not cancel blindly).
- Capped at INFO for `autovacuum`. It is the cure. Killing it is the classic 3am mistake.

**The reconciliation is the part to read first.** If the oldest pin is much younger than `age(datfrozenxid)`, then no live blocker explains the horizon — the problem is vacuum throughput or scheduling (too few workers, cost delay too high, a relation autovacuum never reaches), and the finding says so explicitly. There is no PID to kill, and hunting for one wastes the incident.

A slot legitimately produces **two** rows when both `xmin` and `catalog_xmin` are set: they pin different horizons (data vs catalog), which the `Scope` column shows. `xid` has no ordering operator, so ages are always compared via `age()`, never with `<` — comparing raw `xid` values breaks at wraparound, exactly when it matters.

**Privilege degradation:** without `pg_read_all_stats`, `query` masks to `<insufficient privilege>` and `state`/`backend_type`/`xact_start` go NULL, while `pid` and `backend_xmin` stay visible. This is detected and surfaced rather than looking healthy:

```sql
GRANT pg_monitor TO pgdoctor;  -- includes pg_read_all_stats
```

When `xact_start` is masked, duration falls back to `backend_start` and **overstates** the pin, so those rows are labelled `(from backend_start)`.

### doom-loop

**Severity:** FAIL.

Predictive, and derived from two durable numbers rather than a runtime observation: if any pin's age is at or past the effective trigger, then the next anti-wraparound vacuum is *guaranteed* to complete without advancing `relfrozenxid` past the pin, and `relation_needs_vacanalyze()` re-queues the relation within `autovacuum_naptime` (60s). The result is a non-cancellable, full-table vacuum running indefinitely while age keeps climbing — and vacuum tuning cannot fix it. The pin has to go first.

Remediation is emitted per pin type, always as **single-object commands** — a mass kill pasted by a stressed human at 3am is its own incident:

```sql
-- Replication slot: ESCALATE FIRST. Dropping a Debezium slot forces a re-snapshot.
-- Never drop an active slot.
SELECT pg_drop_replication_slot('debezium_cdc');

-- Backend: cancel, then terminate only if it survives
SELECT pg_cancel_backend(12345);
SELECT pg_terminate_backend(12345);

-- Prepared transaction (verify with the owning application first)
ROLLBACK PREPARED 'stuck-gid-1';
```

For standby feedback the fix is on the replica: `hot_standby_feedback = off` there, or shorter queries. Do not kill the walsender on the primary.

Autovacuum's own `backend_xmin` is excluded: it is a vacuum in progress, not a cause.

## Why `database-*` and `table-*` are not different thresholds

`age(datfrozenxid) ≡ max(age(relfrozenxid))` over the relations `vac_update_datfrozenxid()` counts. The two subchecks measure the same quantity from opposite ends, so they use the **same** thresholds.

The honest relationship between them is lag, not scope: `datfrozenxid` is refreshed only when a vacuum completes, so the database number can legitimately trail the true maximum relation age. A `table-freeze-age` finding without a matching `database-freeze-age` finding is that lag, not a contradiction.

(An earlier version of this check claimed table thresholds were lower "since tables can be vacuumed individually". That was backwards and is gone.)

## The real cliffs

Each row lists the XID GUC and its MultiXact twin, because both clocks have the full set:

| Age (XID / MultiXact) | GUC | What happens |
|---|---|---|
| **150M / 150M** | `vacuum_freeze_table_age`, `vacuum_multixact_freeze_table_age` | Next vacuum becomes *aggressive*: scans the whole relation, not just the visibility-map-dirty part |
| **200M / 400M** | `autovacuum_freeze_max_age`, `autovacuum_multixact_freeze_max_age` | **Anti-wraparound** autovacuum starts, and it is **non-cancellable** |
| **1.6B / 1.6B** | `vacuum_failsafe_age`, `vacuum_multixact_failsafe_age` | Failsafe mode: cost delay disabled, **index vacuuming skipped entirely** — which is why index bloat shows up as a side effect of a wraparound incident |
| **~2.134B** | — | `WARNING: database "x" must be vacuumed within N transactions` on every transaction |
| **~2.1445B** | — | PostgreSQL refuses to assign new XIDs. Writes stop |

Recovery from the hard stop is documented as single-user mode. **That does not exist on RDS**, which is why this check treats the failsafe ages as the design anchor and not 2B.

Two behaviours make the 200M line sharper than it looks:

- **Anti-wraparound vacuum is exempt from the lock-conflict auto-cancel** that a normal autovacuum obeys. A normal autovacuum yields when a conflicting lock request arrives; an anti-wraparound one does not. A queued `ALTER TABLE` therefore turns a harmless background vacuum into a full table lockout: the `ALTER TABLE` waits for the vacuum, and every subsequent query queues behind the `ALTER TABLE`'s pending `AccessExclusiveLock`.
- **`autovacuum_enabled = false` does NOT prevent anti-wraparound vacuum.** Per-table disabling only suppresses ordinary autovacuum. Once the trigger is crossed, PostgreSQL vacuums the relation anyway — which is the right behaviour, and a surprise to anyone who thought they had opted out.

## What a PASS actually means

A PASS means: **no relation is near its trigger and nothing durable pins the horizon at this instant.**

It does **not** mean no incident is coming. XID consumption is a property of the workload, not of the schema — one PL/pgSQL loop can burn 200M XIDs in under 20 minutes, and a PASS 19 minutes before that is still a truthful PASS. That is exactly why headroom is the headline number rather than a verdict: headroom plus a known consumption rate is a time estimate, while a verdict is a snapshot.

## Notes on the data

- **`Size (est)` is a lock-free estimate**, computed entirely from `pg_class`: heap `relpages` + the TOAST relation's `relpages` + the sum of `relpages` over the relation's indexes, times `block_size`. It is **not** `pg_total_relation_size()`, on purpose. That function takes an `AccessShareLock`, and a new `AccessShareLock` request queues behind a *waiting* `AccessExclusiveLock` — so with a 2-second `statement_timeout` this check would SKIP during exactly the DDL pile-up it exists to diagnose. `relpages` is only refreshed by `VACUUM`/`ANALYZE`, so it is stale by definition and `0` on a never-vacuumed relation: those render as `unknown`, never `0 B`.
- **This check reads statistics.** `Last Vacuum` comes from `pg_stat_all_tables` (not `pg_stat_user_tables`, which excludes `pg_catalog` and `pg_toast` and would NULL out the vacuum history of most rows this check returns). Statistics can be reset or stale — use the `statistics-freshness` check to validate their maturity before drawing conclusions from vacuum timestamps.
- The queries work unchanged on PostgreSQL 15-18 and on a standby.

## How to Fix

### Reclaim headroom on a specific relation

```sql
-- TOAST relations are processed through the parent by default
VACUUM (FREEZE, VERBOSE) public.bookings;
```

For very large relations, raise the resources rather than the patience:

```sql
SET maintenance_work_mem = '2GB';
SET max_parallel_maintenance_workers = 4;   -- indexes only, PG13+
VACUUM (FREEZE, VERBOSE) public.bookings;
```

### Make autovacuum keep up

```sql
-- More workers, and a cost delay that lets them actually move
ALTER SYSTEM SET autovacuum_max_workers = 6;
ALTER SYSTEM SET autovacuum_vacuum_cost_delay = '2ms';
ALTER SYSTEM SET autovacuum_vacuum_cost_limit = 2000;
SELECT pg_reload_conf();
```

### Lower the trigger for one relation

Only lowers, never raises — the GUC stays the upper bound:

```sql
ALTER TABLE public.bookings SET (
  autovacuum_freeze_max_age = 100000000
  , autovacuum_multixact_freeze_max_age = 200000000
);
```

Lowering the trigger makes freezing happen earlier and more often. It does not create headroom on its own, and it will not help at all while a pin holds the horizon — check `horizon-blockers` first.

## Related Checks

- `replication-lag` / `replication-slots` — slot health and `max_slot_wal_keep_size` behaviour, the usual source of a permanent `catalog_xmin` pin
- `connection-health` — long-running and idle-in-transaction sessions, the usual source of a temporary pin
- `table-vacuum-health` — whether vacuum is running at all on a relation
- `statistics-freshness` — whether the statistics this check reads can be trusted
- `index-bloat` — the side effect of `vacuum_failsafe_age` skipping index vacuuming

## References

- [Routine Vacuuming: Preventing Transaction ID Wraparound Failures](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-WRAPAROUND)
- [Multixacts and Wraparound](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-MULTIXACT-WRAPAROUND)
- [Automatic Vacuuming (`autovacuum_freeze_max_age`, `vacuum_failsafe_age`)](https://www.postgresql.org/docs/current/runtime-config-autovacuum.html)
- [`pg_stat_activity`](https://www.postgresql.org/docs/current/monitoring-stats.html#MONITORING-PG-STAT-ACTIVITY-VIEW)
- [`pg_replication_slots`](https://www.postgresql.org/docs/current/view-pg-replication-slots.html)
