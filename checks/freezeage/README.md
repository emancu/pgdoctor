# Freeze Age Check

Reports how far past their anti-wraparound trigger PostgreSQL's two 32-bit wraparound counters — transaction IDs and MultiXacts — have drifted, for the connected database and for every VACUUM target in it.

## Background

A low-churn relation accumulates no dead tuples, so nothing makes autovacuum visit it. Its `age(relfrozenxid)` climbs with cluster-wide XID consumption until it hits `autovacuum_freeze_max_age` (200M by default), which triggers a non-cancellable anti-wraparound autovacuum; that freezes the old rows, the age drops, and the climb starts again. **The peak of that cycle is the trigger**, so a relation at 199M against a 200M trigger is at the top of its normal sawtooth, not in trouble — and on a database with hundreds of low-churn relations, most of them sit there most of the time.

`vacuum_freeze_table_age` (150M) does not cap the age either; it only decides whether a vacuum that is *already running* scans the whole relation instead of consulting the visibility map.

What signals trouble is age **exceeding** the trigger: past that point the anti-wraparound vacuum is running and the age should be coming down. If it keeps climbing, either something pins the xmin horizon (a long transaction, a replication slot's `xmin`/`catalog_xmin`, a prepared transaction, standby feedback) or vacuum cannot keep up.

## Thresholds

| | WARN | FAIL | At stock GUCs |
|---|---|---|---|
| XID | `2 × effective autovacuum_freeze_max_age` | `min(4 × trigger, vacuum_failsafe_age)` | 400M / 800M |
| MultiXact | `2 × effective autovacuum_multixact_freeze_max_age` | `min(4 × trigger, vacuum_multixact_failsafe_age)` | 800M / 1.6B |

WARN at 2× means the anti-wraparound vacuum has had a full sawtooth period to pull the age down and has not. FAIL is capped by the failsafe age (1.6B by default), where the cost delay is disabled and **index vacuuming is skipped entirely** — that, not the 2B wall, is the real cliff, because the documented recovery from the hard stop is single-user mode, which does not exist on RDS.

`age()` and `mxid_age()` saturate at 2147483647, so thresholds are clamped there. A per-table reloption can only *lower* a trigger, never raise it (`least(coalesce(reloption, GUC), GUC)`), and every multiple is measured against that effective trigger.

`table-freeze-age` only returns targets already past WARN — the reporting floor is the same expression — so a healthy instance returns zero rows rather than one per relation.

The two counters are independent. A MultiXact is allocated when a row needs a **second** lock holder (the first fits in `xmax`), so concurrent child inserts taking `FOR KEY SHARE` on one hot parent row are the classic generator. **RDS publishes no multixact CloudWatch metric at all**, which is why that clock gets its own findings here.

## What It Checks

### database-freeze-age

`age(datfrozenxid)` for the **connected database only**, as a multiple of `autovacuum_freeze_max_age`. Other `pg_database` rows cannot be vacuumed from this connection, so they are deliberately not reported. (The cluster-wide XID limit is `min(datfrozenxid)` over *every* row, so a neglected `template0` can still trip an RDS `MaximumUsedTransactionIDs` alarm — that is a per-cluster audit, not a per-connection health check.)

### table-freeze-age

`age(relfrozenxid)` per **VACUUM target**, as a multiple of the effective trigger, with `Size (est)` as blast radius:

```
public.bookings (412.0GiB) is at 2.1× its anti-wraparound trigger (420.0M XIDs against 200.0M)
```

Rows are grouped by what the operator would actually run — `VACUUM (FREEZE) public.bookings` — so a TOAST relation never appears as its own unactionable `pg_toast_16452` row; it is folded into its parent, and the group carries the worst age of its members. `--detail debug` names which relation contributed it.

Scope is `relkind IN ('r','m','t')`, the exact set `vac_update_datfrozenxid()` counts (`'p'` has no storage), across all schemas: matviews, `pg_catalog` and orphaned `pg_temp_*` relations age independently of anything in `public`.

### database-multixact-age

`mxid_age(datminmxid)` for the connected database, against `autovacuum_multixact_freeze_max_age`. Guarded with `<> '0'::xid`, because `mxid_age('0'::xid)` returns 2147483647 and would report an instant FAIL on a database that has never allocated a MultiXact.

### table-multixact-age

`mxid_age(relminmxid)` per VACUUM target, same guard, same grouping as `table-freeze-age`.

### horizon-pin

Whether a **durable** pin is holding the xmin horizon: a replication slot's `xmin`/`catalog_xmin`, or a prepared transaction. Both are on-disk state that never resolves itself, so a single read is a snapshot rather than a race — which is why backends, lock waiters and in-flight vacuums are deliberately absent. Reading those needs luck in timing and belongs to a live command (`houston dba xmin`).

This finding exists to answer one question: **kill something, or tune something.** A pin level with `age(datfrozenxid)` — within `max(10M, 5% of the trigger)` — is what the age is waiting on, and the fix is to advance or drop that one object. Nothing level with it means no durable pin explains the age, so the age is autovacuum throughput.

| | WARN | FAIL |
|---|---|---|
| Pin age | `1 × autovacuum_freeze_max_age` | `min(4 × trigger, vacuum_failsafe_age)` |
| Database age, with a level pin | `2 × trigger` | `min(4 × trigger, vacuum_failsafe_age)` |
| Inactive slot | `wal_status` `reserved`/`extended` and pin age ≥ 1M | `wal_status` `unreserved` or `lost`, at any pin age |

A pin warns at `1×`, a full sawtooth period before the age itself warns, because from that point every anti-wraparound vacuum is guaranteed to complete without freezing past it. An **active** slot holding a recent xmin is normal and passes at any recency; an inactive one with invalidated WAL is pure liability, so its age stops mattering. Remediation is always single-object (`SELECT pg_drop_replication_slot('one_slot')`, `ROLLBACK PREPARED 'gid'`) — and dropping a logical slot forces its consumer (Debezium or any other CDC reader) to re-snapshot from scratch, which is a product decision to escalate, not a unilateral DBA action.

## Statistics Requirements

`Last Vacuum` comes from `pg_stat_all_tables`, so vacuum timestamps are only as trustworthy as the statistics window — run `statistics-freshness` before relying on them. The ages themselves come from `pg_class` and `pg_database` and are exact regardless of statistics state.

## How to Fix

A high age has only two causes: something pins the xmin horizon so vacuum runs and advances nothing, or vacuum cannot keep up. `horizon-pin` tells you which.

### For `database-freeze-age`

The database age is the maximum over its relations, so fix the relations — see below. It cannot be vacuumed as a unit, and `VACUUM` at the database level just walks every relation.

### For `table-freeze-age`

Freeze the reported VACUUM target directly, giving it room to work:

```sql
SET maintenance_work_mem = '2GB';
SET max_parallel_maintenance_workers = 4;   -- indexes only, PG13+
VACUUM (FREEZE, VERBOSE) public.bookings;   -- processes its TOAST relation too
```

If many targets are listed, that is autovacuum throughput rather than a per-table problem, and tuning is the fix:

```sql
ALTER SYSTEM SET autovacuum_max_workers = 6;
ALTER SYSTEM SET autovacuum_vacuum_cost_delay = '2ms';
ALTER SYSTEM SET autovacuum_vacuum_cost_limit = 2000;
SELECT pg_reload_conf();
```

Lowering a relation's trigger makes freezing happen earlier and more often, but creates no headroom on its own and does nothing while a pin holds the horizon.

Two behaviours make the trigger sharper than it looks: an anti-wraparound vacuum is **exempt from the lock-conflict auto-cancel** a normal autovacuum obeys, so a queued `ALTER TABLE` turns it into a full table lockout; and **`autovacuum_enabled = false` does not prevent it**.

### For `database-multixact-age` and `table-multixact-age`

Identical remediation — a `VACUUM (FREEZE)` advances both counters. Tune against `autovacuum_multixact_freeze_max_age` rather than the XID trigger, and reduce multixact *generation* by cutting concurrent `FOR KEY SHARE` lockers on one hot parent row.

### For `horizon-pin`

Advance or remove the single object named in the finding:

```sql
SELECT pg_drop_replication_slot('one_slot');   -- forces a full CDC re-snapshot
ROLLBACK PREPARED 'gid';
```

Dropping a logical slot makes its consumer (Debezium or any other CDC reader) re-snapshot from scratch — a product decision to escalate, not a unilateral DBA action. Prefer restarting the consumer so the slot advances on its own. For the live half of the diagnosis — backends, idle-in-transaction sessions, lock waiters — run **`houston dba xmin`** ([houston#1839](https://github.com/fresha/houston/issues/1839)); those need luck in timing and are not health checks. While a pin sits at or past the trigger, vacuum tuning cannot help: every anti-wraparound vacuum completes without advancing past it and is re-queued within `autovacuum_naptime`.

## Notes

- A PASS means no relation is more than 2× past its trigger *at this instant*. XID consumption is a property of the workload: a single PL/pgSQL loop was measured burning 80,001 XIDs in 0.4 seconds, so watch the multiple over time rather than treating one PASS as a forecast.
- **`Size (est)` is a lock-free `relpages` estimate** (heap + TOAST + indexes × `block_size`), because `pg_total_relation_size()` takes an `AccessShareLock` that would queue behind a waiting `AccessExclusiveLock` and time this check out during a DDL pile-up. It is stale until the next `VACUUM`/`ANALYZE`, and `unknown` when `relpages` is 0.
- **PostgreSQL 14 is the floor**, because `vacuum_failsafe_age` and `vacuum_multixact_failsafe_age` were added there. `horizon-pin` reads `wal_status` (PG13+) and never `pg_replication_slots.inactive_since` (PG17+), so slot recency is the age of the pinned xid. Verified by running all three queries on 14.23, 13.23 and 17.10 — on 13 the GUCs are absent and the `COALESCE` supplies the documented 1.6B default, so an older major degrades instead of erroring.

## Related Checks

- `table-vacuum-health` — whether vacuum is running at all on a relation
- `replication-lag` / `replication-slots` — the usual source of a permanent `catalog_xmin` pin
- `connection-health` — long-running and idle-in-transaction sessions, the usual source of a temporary pin
- `index-bloat` — the side effect of the failsafe skipping index vacuuming

## References

- [Preventing Transaction ID Wraparound Failures](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-WRAPAROUND)
- [Multixacts and Wraparound](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-MULTIXACT-WRAPAROUND)
