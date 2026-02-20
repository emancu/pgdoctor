# Vacuum Settings Check

Verifies that PostgreSQL autovacuum and maintenance settings are properly configured to prevent table bloat and maintain database health.

## What It Checks

### autovacuum_vacuum_scale_factor

Validates the threshold for triggering autovacuum on tables.

**Severity:**
- WARN: Value > 0.2 (too high, allows excessive bloat)
- WARN: Value < 0.02 (too low, causes excessive vacuum overhead)
- OK: Between 0.02 and 0.2

**PostgreSQL default:** 0.2

**Recommended:** 0.1-0.2

**Why this matters:** Controls when autovacuum runs based on percentage of dead tuples. Values > 0.2 allow large tables to accumulate excessive bloat before vacuuming. Values < 0.02 cause unnecessary vacuum overhead.

**Formula:** `autovacuum triggers when dead_tuples > (total_tuples * scale_factor) + vacuum_threshold`

### autovacuum_analyze_scale_factor

Validates the threshold for triggering autoanalyze (statistics updates).

**Severity:**
- WARN: Value > 0.1 (too high, leads to stale statistics)
- WARN: Value < 0.01 (too low, causes excessive analyze overhead)
- OK: Between 0.01 and 0.1

**PostgreSQL default:** 0.1

**Recommended:** 0.05-0.1

**Why this matters:** Controls when statistics are updated based on percentage of changed tuples. Stale statistics lead to poor query plans. Too-frequent analyze wastes resources.

### autovacuum_max_workers

Validates the number of parallel autovacuum worker processes.

**Severity:**
- FAIL: Workers = 0 (autovacuum disabled, will cause bloat and wraparound)
- WARN: Workers = 1 (critically low, cannot keep up with multiple tables)
- WARN: Workers > 10 (excessive, causes I/O contention)
- WARN: Workers = 3 on large instances (>=32 vCPU, likely bottleneck)
- OK: Workers between 2 and 10

**PostgreSQL default:** 3

**Recommended:** 3-6 for most workloads, up to 6 for very large instances

**Why this matters:** Controls parallelism of autovacuum operations. Too few workers create bottlenecks on busy databases with many tables. Too many workers cause I/O contention.

**Critical:** `Total autovacuum RAM = maintenance_work_mem × autovacuum_max_workers`

### maintenance_work_mem

Validates memory allocated for maintenance operations (VACUUM, CREATE INDEX, REINDEX).

**Severity:**
- FAIL: Total budget (maintenance_work_mem × autovacuum_max_workers) > 25% of RAM
- WARN: Total budget > 12.5% of RAM
- WARN: Value < 32MB (below half PostgreSQL default, causes slow VACUUM)
- WARN: Value > 4GB (excessive, diminishing returns above 2GB)
- WARN: Value = 64MB on large instances (>=64GB RAM, likely too low)
- OK: Reasonable value with safe total budget

**PostgreSQL default:** 64MB

**Recommended:** 256MB-1GB depending on instance size

**Why this matters:** Controls memory available for tracking dead tuples during VACUUM. Too low causes multiple VACUUM passes (check logs for "index scans: 2+"). Too high wastes memory.

**CRITICAL:** PostgreSQL has a 1GB limit for tracking dead tuples. Values > 1GB only help CREATE INDEX, not VACUUM.

**Formula:** `Total autovacuum RAM = maintenance_work_mem × autovacuum_max_workers` (should be < 12.5-25% of total RAM)

### vacuum_cost_delay

Validates the throttling delay for vacuum operations to reduce I/O impact.

**Severity:**
- WARN: Value > 20ms (may throttle vacuum too much)
- OK: <= 20ms

**PostgreSQL default:** 2ms

**Recommended:** 0-10ms

**Why this matters:** Controls sleep time between vacuum I/O operations. Higher values reduce I/O impact but slow vacuum progress.

### vacuum_cost_limit

Validates the cost limit per vacuum round before sleeping.

**Severity:**
- WARN: Value < 200 (may throttle too much)
- WARN: Value > 10000 (may cause I/O spikes)
- OK: Between 200 and 10000

**PostgreSQL default:** 200

**Recommended:** 200-2000

**Why this matters:** Controls how much I/O vacuum can do before sleeping. Higher values make vacuum faster but more I/O intensive.

### work_mem

Validates memory allocated for query operations (sorts, hash tables).

**Severity:**
- FAIL: Value < 4MB (critically low, causes excessive temp file usage)
- FAIL: Worst-case usage (work_mem × max_connections) > 80% of RAM
- WARN: Worst-case usage > 50% of RAM
- WARN: Current usage (work_mem × active_connections) > 40% of RAM
- OK: Reasonable value with safe worst-case usage

**PostgreSQL default:** 4MB

**Recommended:** 16-64MB for most workloads

**Why this matters:** Each query operation (sort, hash) can use work_mem, and complex queries use it multiple times. Too low causes disk I/O from temp files. Too high risks out-of-memory errors.

**HIGH RISK:** `Worst-case = work_mem × max_connections` can spike during connection surges.

## Why It Matters

Proper vacuum settings prevent serious database problems:
- **Table bloat**: Dead tuples waste disk space and slow queries
- **Index bloat**: Unused index entries cause fragmentation
- **Transaction ID wraparound**: Can cause database shutdown
- **Stale statistics**: Lead to poor query plans and performance
- **Lock contention**: Inefficient vacuum can block queries

Without proper autovacuum configuration:
- Tables grow unnecessarily large
- Query performance degrades over time
- Emergency manual vacuum becomes necessary
- Risk of catastrophic transaction ID wraparound

## Memory Planning (Critical)

### Total RAM Budget

**The most important concept**: When autovacuum runs, it allocates memory per worker:

```
Total autovacuum RAM = maintenance_work_mem × autovacuum_max_workers
```

**Example scenarios:**
- `maintenance_work_mem = 256MB` × `autovacuum_max_workers = 3` = **768MB total**
- `maintenance_work_mem = 1GB` × `autovacuum_max_workers = 4` = **4GB total**
- `maintenance_work_mem = 10GB` × `autovacuum_max_workers = 3` = **30GB total** (dangerous!)

**Guidelines:**
- Keep total budget under **12.5-25% of available RAM**
- Small instances (<8GB RAM): Be very conservative
- Manual VACUUM and CREATE INDEX also use this memory (on top of autovacuum)

### PostgreSQL's 1GB Internal Limit

PostgreSQL has a **1GB limit for tracking dead tuples** during VACUUM:
- Setting `maintenance_work_mem > 1GB` helps CREATE INDEX on huge tables
- But doesn't improve VACUUM's dead tuple tracking beyond 1GB
- Values between 1-2GB are still useful for mixed workloads

### autovacuum_work_mem Parameter

You can set `autovacuum_work_mem` separately to control autovacuum independently:
```sql
-- Separate autovacuum memory from manual operations
ALTER SYSTEM SET autovacuum_work_mem = '256MB';  -- For autovacuum
ALTER SYSTEM SET maintenance_work_mem = '2GB';   -- For CREATE INDEX, manual VACUUM
```

**Default**: `-1` (uses `maintenance_work_mem` value)

### Signs Your Settings Are Too Low

Check PostgreSQL logs for autovacuum messages:
- **"index scans: 2"** or higher = ran out of memory, multiple passes needed
- **"index scans: 0"** = good, single pass
- Increase `maintenance_work_mem` if you see multiple index scans

### work_mem vs maintenance_work_mem

| Parameter | Used By | Multiplier | Risk Factor |
|-----------|---------|------------|-------------|
| `work_mem` | Query sorts/hashes | `max_connections` | **High** (can OOM easily) |
| `maintenance_work_mem` | VACUUM, CREATE INDEX | `autovacuum_max_workers` | **Medium** (fewer workers) |

**Key difference**: `work_mem` can multiply by 100+ connections, `maintenance_work_mem` typically by 3-6 workers.

## How to Fix

Adjust vacuum settings in `postgresql.conf` or via `ALTER SYSTEM`:

```sql
-- Recommended settings for production databases
ALTER SYSTEM SET autovacuum_vacuum_scale_factor = 0.1;      -- Vacuum when 10% dead tuples
ALTER SYSTEM SET autovacuum_analyze_scale_factor = 0.05;    -- Analyze when 5% changed
ALTER SYSTEM SET autovacuum_max_workers = 4;                -- 4 parallel workers
ALTER SYSTEM SET maintenance_work_mem = '512MB';            -- Memory for maintenance
ALTER SYSTEM SET vacuum_cost_delay = 5;                     -- 5ms throttle delay
ALTER SYSTEM SET vacuum_cost_limit = 300;                   -- Cost limit per round
ALTER SYSTEM SET work_mem = '64MB';                         -- Memory for queries

-- Reload configuration
SELECT pg_reload_conf();
```

### For `autovacuum_vacuum_scale_factor`

**PostgreSQL default: `0.2`**
- Use smaller factor for Big tables (+10M)
- Lower values (0.01-0.2) mean more frequent vacuum
- Values > 0.2 may allow too much bloat on large tables

### For `autovacuum_analyze_scale_factor`

**PostgreSQL default: `0.1`**
- More frequent than vacuum is typical (0.05-0.1)
- Values < 0.01 may cause excessive analyze overhead
- Values > 0.1 may lead to stale statistics

### For `autovacuum_max_workers`

**PostgreSQL default: `3`**
- Default of 3 workers is sufficient for most workloads
- Consider: `Total RAM = maintenance_work_mem × autovacuum_max_workers`
- Values of 0 disable autovacuum (dangerous), 1 is too low, >10 rarely beneficial

### For `maintenance_work_mem`

**PostgreSQL default: `64MB`**
- **CRITICAL**: `Total budget = maintenance_work_mem × autovacuum_max_workers`
- Keep total budget under 12.5-25% of available RAM
- Values < 32MB may cause slow VACUUM with multiple passes
- Values > 1GB show diminishing returns (PostgreSQL has 1GB dead tuple tracking limit)
- Consider `autovacuum_work_mem` to separate autovacuum from manual operations

### For `vacuum_cost_delay`

**PostgreSQL default: `2ms`**
- Controls throttling to reduce I/O impact
- Values > 20ms may slow vacuum too much
- Adjust based on disk performance and workload

### For `vacuum_cost_limit`

**PostgreSQL default: `200`**
- Controls aggressiveness of vacuum
- Values < 200 may throttle too much
- Values > 10000 may cause I/O spikes

### For `work_mem`

**PostgreSQL default: `4MB`**
- **HIGH RISK**: Multiplies by `max_connections`
- Total `worst-case = work_mem × max_connections`
- Keep `worst-case` under 50-80% of available RAM
- Values < 4MB cause excessive temp file usage
- Values > 64MB risky without RAM awareness

### For Individual Tables

Override settings for specific tables:

```sql
-- More aggressive vacuum for high-churn table
ALTER TABLE outbox_events SET (
  autovacuum_vacuum_scale_factor = 0.05,
  autovacuum_analyze_scale_factor = 0.02
);
```

### Verification

Check current settings:

```sql
SHOW autovacuum_vacuum_scale_factor;
SHOW autovacuum_analyze_scale_factor;
SHOW autovacuum_max_workers;
```

Monitor autovacuum activity:

```sql
SELECT schemaname, relname, last_vacuum, last_autovacuum,
       n_dead_tup, n_live_tup
FROM pg_stat_user_tables
ORDER BY n_dead_tup DESC
LIMIT 10;
```

## References

- [PostgreSQL Documentation: Autovacuum](https://www.postgresql.org/docs/current/routine-vacuuming.html#AUTOVACUUM)
- [PostgreSQL Documentation: VACUUM](https://www.postgresql.org/docs/current/sql-vacuum.html)
- [PostgreSQL Documentation: pg_stat_user_tables](https://www.postgresql.org/docs/current/monitoring-stats.html#MONITORING-PG-STAT-ALL-TABLES-VIEW)
- [PostgreSQL Parameters Documentation](https://postgresqlco.nf/)