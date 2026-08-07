# Freeze Age Check

Reports how far past their anti-wraparound trigger PostgreSQL's two 32-bit wraparound counters, transaction IDs and MultiXacts, have drifted for the connected database and for every VACUUM target in it.

## Background

A low-churn relation accumulates no dead tuples, so nothing makes autovacuum visit it. Its `age(relfrozenxid)` climbs with cluster-wide XID consumption until it reaches `autovacuum_freeze_max_age`, which fires a non-cancellable anti-wraparound vacuum; the age drops and the climb restarts. The peak of that sawtooth is the trigger, so 199M against a 200M trigger is the normal top of the cycle. An earlier version of this check missed that and fired on 422 healthy relations. Age *exceeding* the trigger is the signal: past that point the anti-wraparound vacuum is running and the age should be falling, so if it keeps climbing either a durable pin holds the xmin horizon or vacuum cannot keep up.

## Thresholds

| | WARN | FAIL | At stock GUCs |
|---|---|---|---|
| XID | `2x` effective `autovacuum_freeze_max_age` | `min(4x trigger, vacuum_failsafe_age)` | 400M / 800M |
| MultiXact | `2x` effective `autovacuum_multixact_freeze_max_age` | `min(4x trigger, vacuum_multixact_failsafe_age)` | 800M / 1.6B |

WARN at `2x` gives the anti-wraparound vacuum a full sawtooth period to pull the age down. FAIL is capped at the failsafe age, where the cost delay is disabled and index vacuuming is skipped entirely; that is the real cliff, not the 2B wall, because the documented recovery from the hard stop is single-user mode and RDS has none. A per-table reloption can only lower a trigger, never raise it, and every multiple is measured against that effective trigger.

## What It Checks

### database-freeze-age

`age(datfrozenxid)` for the connected database only. Other `pg_database` rows cannot be vacuumed from this connection.

### table-freeze-age

`age(relfrozenxid)` per VACUUM target (`relkind IN ('r','m','t')`, all schemas), with `Size (est)` as blast radius. Rows are grouped by what you would actually run, `VACUUM (FREEZE) public.bookings`, so a TOAST relation folds into its parent and the group carries the worst age of its members; `--detail debug` names the contributor. Only targets already past WARN are returned.

### database-multixact-age

`mxid_age(datminmxid)` for the connected database, guarded with `<> '0'::xid` because `mxid_age('0'::xid)` returns 2147483647 and would FAIL a database that has never allocated a MultiXact.

### table-multixact-age

`mxid_age(relminmxid)` per VACUUM target, same guard and same grouping.

### horizon-pin

Durable pins on the xmin horizon: a replication slot's `xmin`/`catalog_xmin`, or a prepared transaction. Both are on-disk state that never resolves itself, so a single read is a snapshot rather than a race. The finding answers one question: kill something, or tune something.

| | WARN | FAIL |
|---|---|---|
| Pin age | `1x autovacuum_freeze_max_age` | `min(4x trigger, vacuum_failsafe_age)` |
| Inactive slot | `wal_status` `reserved`/`extended` and pin age >= 1M | `wal_status` `unreserved` or `lost`, at any pin age |

A pin warns at `1x`, one sawtooth period before the age itself warns, because from there every anti-wraparound vacuum is guaranteed to complete without freezing past it. An active slot holding a recent xmin is normal CDC operation and passes at any recency. Coincidence, a pin within `max(10M, 5% of trigger)` of the database age, decides the message and not the severity: a level pin is what the age is waiting on, and no level pin means the age is autovacuum throughput. Backends, idle-in-transaction sessions and lock waiters are absent on purpose: reading them needs luck in timing, so they belong to `houston dba xmin`.

## Statistics Requirements

`Last Vacuum` comes from `pg_stat_all_tables`, so it is only as trustworthy as the statistics window; run `statistics-freshness` before relying on it. The ages come from `pg_class` and `pg_database` and are exact.

## How to Fix

### For `database-freeze-age`

The database age is the maximum over its relations, so fix the relations below. It cannot be vacuumed as a unit.

### For `table-freeze-age`

Freeze the reported target directly, with room to work:

```sql
SET maintenance_work_mem = '2GB';
VACUUM (FREEZE, VERBOSE) public.bookings;   -- processes its TOAST relation too
```

Many targets at once is autovacuum throughput rather than a per-table problem: raise `autovacuum_max_workers` and `autovacuum_vacuum_cost_limit`, drop `autovacuum_vacuum_cost_delay`, then `SELECT pg_reload_conf()`. Lowering a relation's trigger freezes it earlier and more often, but creates no headroom on its own and does nothing while a pin holds the horizon.

### For `database-multixact-age` and `table-multixact-age`

Same remediation, since a `VACUUM (FREEZE)` advances both counters. Tune against `autovacuum_multixact_freeze_max_age`, and cut MultiXact generation by reducing concurrent `FOR KEY SHARE` lockers on one hot parent row.

### For `horizon-pin`

Advance or remove the single object the finding names, one command at a time: `SELECT pg_drop_replication_slot('one_slot')`, `ROLLBACK PREPARED 'gid'`. Prefer restarting the consumer so the slot advances on its own. Dropping a logical slot forces its CDC reader to re-snapshot from scratch, a product decision to escalate rather than a unilateral DBA action. For a physical slot, restore the standby's replay first and drop the slot only once the standby is confirmed gone. While a pin sits at or past the trigger, tuning cannot help: every anti-wraparound vacuum completes without advancing past it and is re-queued within `autovacuum_naptime`.

## Notes

- `Size (est)` is a lock-free `relpages` estimate. `pg_total_relation_size()` takes an `AccessShareLock` that queues behind waiting DDL and would time this check out during a lock pile-up. It is stale until the next `VACUUM`/`ANALYZE`, and `unknown` when `relpages` is 0.
- Anti-wraparound vacuum is exempt from the lock-conflict auto-cancel a normal autovacuum obeys, so a queued `ALTER TABLE` turns it into a table lockout. `autovacuum_enabled = false` does not prevent it.
- PostgreSQL 14 is the floor, because `vacuum_failsafe_age` and `vacuum_multixact_failsafe_age` were added there. `horizon-pin` never reads `inactive_since` (PG17+), so slot recency is the age of the pinned xid.

## Related Checks

- `table-vacuum-health` - whether vacuum is running at all on a relation
- `replication-lag` / `replication-slots` - the usual source of a permanent `catalog_xmin` pin
- `connection-health` - long-running and idle-in-transaction sessions
- `index-bloat` - the side effect of the failsafe skipping index vacuuming

## References

- [Preventing Transaction ID Wraparound Failures](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-WRAPAROUND)
- [Multixacts and Wraparound](https://www.postgresql.org/docs/current/routine-vacuuming.html#VACUUM-FOR-MULTIXACT-WRAPAROUND)
