# Freeze Age Check

Reports how far past their anti-wraparound trigger PostgreSQL's two 32-bit wraparound counters — transaction IDs and MultiXacts — have drifted, for the connected database and for every VACUUM target in it.

## Background: age is a sawtooth, and its peak is the trigger

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

### database-multixact-age / table-multixact-age

The same two findings against `datminmxid` and `relminmxid`. Both queries guard with `<> '0'::xid`, because `mxid_age('0'::xid)` returns 2147483647 and would report an instant FAIL on a database that has never allocated a MultiXact.

## How to Fix

A high age means vacuum is not advancing `relfrozenxid`, and there are only two causes.

**Something pins the xmin horizon.** Vacuum can only freeze rows older than the oldest snapshot in the system, so a long transaction, slot, or prepared transaction makes vacuum run, succeed, and advance nothing. Finding it is a live investigation, not a health check: run **`houston dba xmin`** ([houston#1839](https://github.com/fresha/houston/issues/1839)). The division of labour is that this check says *you are close to the cliff* and that says *here is what to kill*. If a pin is at or past the trigger, every subsequent anti-wraparound vacuum is guaranteed to complete without advancing past it and to be re-queued within `autovacuum_naptime` — vacuum tuning cannot fix that.

**Vacuum cannot keep up.** Freeze the target directly, giving it room to work:

```sql
SET maintenance_work_mem = '2GB';
SET max_parallel_maintenance_workers = 4;   -- indexes only, PG13+
VACUUM (FREEZE, VERBOSE) public.bookings;   -- processes its TOAST relation too
```

```sql
ALTER SYSTEM SET autovacuum_max_workers = 6;
ALTER SYSTEM SET autovacuum_vacuum_cost_delay = '2ms';
ALTER SYSTEM SET autovacuum_vacuum_cost_limit = 2000;
SELECT pg_reload_conf();
```

Lowering a relation's trigger makes freezing happen earlier and more often, but creates no headroom on its own and does nothing while a pin holds the horizon.

Two behaviours make the trigger sharper than it looks: an anti-wraparound vacuum is **exempt from the lock-conflict auto-cancel** a normal autovacuum obeys, so a queued `ALTER TABLE` turns it into a full table lockout; and **`autovacuum_enabled = false` does not prevent it**.

## Notes

- A PASS means no relation is more than 2× past its trigger *at this instant*. XID consumption is a property of the workload: a single PL/pgSQL loop was measured burning 80,001 XIDs in 0.4 seconds, so watch the multiple over time rather than treating one PASS as a forecast.
- **`Size (est)` is a lock-free `relpages` estimate** (heap + TOAST + indexes × `block_size`), because `pg_total_relation_size()` takes an `AccessShareLock` that would queue behind a waiting `AccessExclusiveLock` and time this check out during a DDL pile-up. It is stale until the next `VACUUM`/`ANALYZE`, and `unknown` when `relpages` is 0.
- **This check reads statistics.** `Last Vacuum` comes from `pg_stat_all_tables`; use `statistics-freshness` before trusting vacuum timestamps.
- **PostgreSQL 14 is the floor**, because `vacuum_failsafe_age` and `vacuum_multixact_failsafe_age` were added there. Verified by running both queries on 14.23, 13.23 and 17.10 — on 13 the GUCs are absent and the `COALESCE` supplies the documented 1.6B default, so an older major degrades instead of erroring.

## Related Checks

- `table-vacuum-health` — whether vacuum is running at all on a relation
- `replication-lag` / `replication-slots` — the usual source of a permanent `catalog_xmin` pin
- `connection-health` — long-running and idle-in-transaction sessions, the usual source of a temporary pin
- `index-bloat` — the side effect of the failsafe skipping index vacuuming

## References

- [Preventing Transaction ID Wraparound Failures](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-WRAPAROUND)
- [Multixacts and Wraparound](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-MULTIXACT-WRAPAROUND)
