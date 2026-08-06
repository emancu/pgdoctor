# Freeze Age Check

Monitors how far past their anti-wraparound trigger PostgreSQL's two 32-bit wraparound counters — transaction IDs and MultiXacts — have drifted, for the connected database and for every VACUUM target in it.

## Background: age is a sawtooth, and its peak is the trigger

A relation's `age(relfrozenxid)` does not sit still and it does not grow without bound. It is a sawtooth:

1. A low-churn relation accumulates no dead tuples, so nothing makes autovacuum visit it.
2. Its age climbs with cluster-wide XID consumption until it reaches `autovacuum_freeze_max_age` (200M by default).
3. That triggers an **anti-wraparound autovacuum**, which freezes the old rows and advances `relfrozenxid`.
4. The age drops, and the climb starts again.

**The peak of that cycle is the trigger.** A relation at 199M against a 200M trigger is at the top of its normal sawtooth, not in trouble — and on a database with hundreds of low-churn relations, most of them sit there most of the time. This is why the check does not warn near the trigger: it would warn on the upper quarter of every relation's normal cycle, on every healthy instance.

`vacuum_freeze_table_age` (150M) does not cap the age either. It only decides whether a vacuum that is *already running* scans the whole relation instead of consulting the visibility map. It is a behaviour switch inside a vacuum, not a deadline.

What actually signals trouble is age **exceeding** the trigger. Past that point the non-cancellable anti-wraparound vacuum is running, and the age should be coming down. If it keeps climbing instead, something is stopping vacuum from advancing `relfrozenxid`: a long transaction, a replication slot holding `xmin`/`catalog_xmin`, a prepared transaction, standby feedback — or vacuum simply cannot keep up.

## Thresholds

| | WARN | FAIL | At stock GUCs |
|---|---|---|---|
| XID | `2 × effective autovacuum_freeze_max_age` | `min(4 × trigger, vacuum_failsafe_age)` | 400M / 800M |
| MultiXact | `2 × effective autovacuum_multixact_freeze_max_age` | `min(4 × trigger, vacuum_multixact_failsafe_age)` | 800M / 1.6B |

Both counters use the same derivation against their own GUCs. The counters are independent: a MultiXact is allocated when a row needs a **second** lock holder (the first fits in `xmax`), so concurrent child inserts taking `FOR KEY SHARE` on one hot parent row are the classic generator. **RDS publishes no multixact CloudWatch metric at all** — `MaximumUsedTransactionIDs` tracks XIDs only — so that clock is invisible to normal alarms, which is why it gets its own findings here.

**Why 2×:** one full sawtooth period past the trigger means the anti-wraparound vacuum has had the whole span of a normal cycle to pull the age down and has not. That is no longer the sawtooth; that is a relation losing ground.

**Why FAIL at 4× capped by the failsafe age:** the failsafe (1.6B by default) is the real cliff — cost delay disabled and **index vacuuming skipped entirely**. 2B is not a usable design anchor: the documented recovery from the hard stop is single-user mode, which **does not exist on RDS**.

`age()` and `mxid_age()` saturate at 2147483647, so thresholds are clamped there.

**Per-table reloptions can only lower the trigger, never raise it.** `ALTER TABLE ... SET (autovacuum_freeze_max_age = N)` is applied as `least(coalesce(N, GUC), GUC)`, matching PostgreSQL, and every multiple is measured against that effective trigger — the `Trigger` column shows it, marked `(reloption)` when an override is in play.

## Reporting floor

`table-freeze-age` only returns targets already past WARN (`2 × trigger`). A healthy instance returns **zero rows** instead of shipping every relation across the wire — the floor is the same expression as the WARN threshold, so nothing that would be flagged is filtered out. When more than 50 targets are above the floor (pg_partman partitions age in lockstep), the count is reported alongside the worst 50.

## What It Checks

### database-freeze-age

`age(datfrozenxid)` for the **connected database only**, as a multiple of `autovacuum_freeze_max_age`.

Other rows of `pg_database` — `template0`, `template1`, and anything else in the cluster — are deliberately not reported. They cannot be vacuumed from this connection, so a finding about them is one nobody can act on from here. (Note that the cluster-wide XID limit is `min(datfrozenxid)` over *every* row of `pg_database`, so a neglected `template0` can still trip an RDS `MaximumUsedTransactionIDs` alarm while this check passes. That is a per-cluster audit, not a per-connection health check.)

### table-freeze-age

`age(relfrozenxid)` per **VACUUM target**, as a multiple of the effective trigger, with `Size (est)` as blast radius:

```
public.bookings (412.0GiB) is at 2.1× its anti-wraparound trigger (420.0M XIDs against 200.0M)
```

Rows are grouped by what the operator would actually run:

```sql
VACUUM (FREEZE, VERBOSE) public.bookings;  -- processes its TOAST relation by default
```

A TOAST relation is only ever vacuumed through its parent, so `pg_toast.pg_toast_16452` never appears as its own row — it is folded into `public.bookings`, and the group carries the worst age of its members. `--detail debug` names which relation in the group contributed it.

Scope is `relkind IN ('r','m','t')` — the exact set `vac_update_datfrozenxid()` counts. `'p'` is excluded (no storage). There is **no schema filter**: an abandoned logical replication slot pins `catalog_xmin` on `pg_catalog` relations, and matviews and orphaned `pg_temp_*` relations age independently of anything in `public`.

### database-multixact-age

Same, against `pg_database.datminmxid` and the MultiXact GUCs.

`mxid_age('0'::xid)` returns 2147483647, so the query guards with `datminmxid <> '0'::xid`. Without that guard a database that has never allocated a MultiXact would report an instant FAIL.

### table-multixact-age

Same, against `pg_class.relminmxid`, with the same `<> '0'::xid` guard.

## When a finding fires: what to do

A high age means vacuum is not advancing `relfrozenxid`. There are only two families of cause, and they need different tools.

**1. Something pins the xmin horizon.** Vacuum can only freeze rows older than the oldest snapshot in the system, so a long transaction, a replication slot's `xmin`/`catalog_xmin`, a prepared transaction or standby feedback will make vacuum run, succeed, and advance nothing.

Finding and killing that object is a live investigation, not a health check — it belongs to **`houston dba xmin`** ([houston#1839](https://github.com/fresha/houston/issues/1839)), which lists the longest-running transactions, idle-in-transaction sessions, prepared 2PC transactions, slots holding `xmin`/`catalog_xmin`, and in-progress autovacuums from `pg_stat_progress_vacuum`. The division of labour: this check says *you are close to the cliff*, `houston dba xmin` says *here is what to kill*.

Start there. If a pin is at or past the trigger, the next anti-wraparound vacuum is *guaranteed* to complete without advancing `relfrozenxid` past it and to be re-queued within `autovacuum_naptime` — a non-cancellable, full-table vacuum looping indefinitely while the age keeps climbing. Vacuum tuning cannot fix that; the pin has to go first.

**2. Vacuum cannot keep up.** If nothing pins the horizon, the problem is throughput or scheduling.

```sql
-- Freeze a specific target now (TOAST is processed through the parent)
VACUUM (FREEZE, VERBOSE) public.bookings;
```

For very large relations, raise the resources rather than the patience:

```sql
SET maintenance_work_mem = '2GB';
SET max_parallel_maintenance_workers = 4;   -- indexes only, PG13+
VACUUM (FREEZE, VERBOSE) public.bookings;
```

```sql
-- More workers, and a cost delay that lets them move
ALTER SYSTEM SET autovacuum_max_workers = 6;
ALTER SYSTEM SET autovacuum_vacuum_cost_delay = '2ms';
ALTER SYSTEM SET autovacuum_vacuum_cost_limit = 2000;
SELECT pg_reload_conf();
```

Lowering a relation's trigger makes freezing happen earlier and more often, but it creates no headroom on its own and does nothing while a pin holds the horizon:

```sql
ALTER TABLE public.bookings SET (
  autovacuum_freeze_max_age = 100000000
  , autovacuum_multixact_freeze_max_age = 200000000
);
```

## The real cliffs

Each row lists the XID GUC and its MultiXact twin, because both clocks have the full set. Only the last three are failure states — the first two are normal operation:

| Age (XID / MultiXact) | GUC | What happens |
|---|---|---|
| **150M / 150M** | `vacuum_freeze_table_age`, `vacuum_multixact_freeze_table_age` | A vacuum that runs from here scans the whole relation instead of using the visibility map. Normal; not a deadline |
| **200M / 400M** | `autovacuum_freeze_max_age`, `autovacuum_multixact_freeze_max_age` | **Anti-wraparound** autovacuum starts, and it is **non-cancellable**. This is the expected peak of the sawtooth |
| **1.6B / 1.6B** | `vacuum_failsafe_age`, `vacuum_multixact_failsafe_age` | Failsafe mode: cost delay disabled, **index vacuuming skipped entirely** — which is why index bloat shows up as a side effect of a wraparound incident |
| **~2.134B** | — | `WARNING: database "x" must be vacuumed within N transactions` on every transaction |
| **~2.1445B** | — | PostgreSQL refuses to assign new XIDs. Writes stop |

Recovery from the hard stop is documented as single-user mode. **That does not exist on RDS**, which is why the failsafe ages are the design anchor and not 2B.

Two behaviours make the 200M line sharper than it looks:

- **Anti-wraparound vacuum is exempt from the lock-conflict auto-cancel** that a normal autovacuum obeys. A normal autovacuum yields when a conflicting lock request arrives; an anti-wraparound one does not. A queued `ALTER TABLE` therefore turns a harmless background vacuum into a full table lockout: the `ALTER TABLE` waits for the vacuum, and every subsequent query queues behind its pending `AccessExclusiveLock`.
- **`autovacuum_enabled = false` does NOT prevent anti-wraparound vacuum.** Per-table disabling only suppresses ordinary autovacuum. Once the trigger is crossed, PostgreSQL vacuums the relation anyway — correct behaviour, and a surprise to anyone who thought they had opted out.

## What a PASS actually means

A PASS means: **no relation is more than 2× past its trigger at this instant.**

It does **not** mean no incident is coming. XID consumption is a property of the workload, not of the schema: a single PL/pgSQL loop was measured consuming 80,001 XIDs in 0.4 seconds, so one background job can burn 200M XIDs in under 20 minutes, and a PASS 19 minutes earlier was still truthful. Multiples, not verdicts, are the number to watch over time.

## Notes on the data

- **`Size (est)` is a lock-free estimate**, computed entirely from `pg_class`: the target's heap `relpages` + its TOAST relation's `relpages` + the sum of `relpages` over its indexes, times `block_size`. It is **not** `pg_total_relation_size()`, on purpose. That function takes an `AccessShareLock`, and a new `AccessShareLock` request queues behind a *waiting* `AccessExclusiveLock` — so with a 2-second `statement_timeout` this check would SKIP during exactly the DDL pile-up it exists to diagnose. `relpages` is only refreshed by `VACUUM`/`ANALYZE`, so it is stale by definition and `0` on a never-vacuumed relation: those render as `unknown`, never `0 B`.
- **This check reads statistics.** `Last Vacuum` comes from `pg_stat_all_tables` (not `pg_stat_user_tables`, which excludes `pg_catalog` and `pg_toast` and would NULL out the vacuum history of most rows returned here). Statistics can be reset or stale — use the `statistics-freshness` check before drawing conclusions from vacuum timestamps.

## PostgreSQL version support

Verified by running both queries against **PostgreSQL 14.23, 13.23 and 17.10**.

**14 is the meaningful floor**, because `vacuum_failsafe_age` and `vacuum_multixact_failsafe_age` were added in 14 and the FAIL threshold reads them. On **13** both queries still execute — the GUCs are simply absent, and the `COALESCE` in the settings aggregate supplies the documented 1.6B default, so an older major degrades to the documented value instead of erroring. Everything else used here (`mxid_age()`, `pg_class.relminmxid`, `pg_stat_all_tables`, `reloptions`, `FILTER`, `array_agg(... ORDER BY ...)`) predates 13.

## Related Checks

- `table-vacuum-health` — whether vacuum is running at all on a relation
- `replication-lag` / `replication-slots` — slot health and `max_slot_wal_keep_size`, the usual source of a permanent `catalog_xmin` pin
- `connection-health` — long-running and idle-in-transaction sessions, the usual source of a temporary pin
- `statistics-freshness` — whether the statistics this check reads can be trusted
- `index-bloat` — the side effect of the failsafe skipping index vacuuming

## References

- [Routine Vacuuming: Preventing Transaction ID Wraparound Failures](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-WRAPAROUND)
- [Multixacts and Wraparound](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-MULTIXACT-WRAPAROUND)
- [Automatic Vacuuming (`autovacuum_freeze_max_age`, `vacuum_failsafe_age`)](https://www.postgresql.org/docs/current/runtime-config-autovacuum.html)
