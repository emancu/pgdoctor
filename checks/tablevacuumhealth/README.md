# Table Vacuum Health Check

Monitors per-table autovacuum configuration and activity to identify maintenance issues.

## Background

PostgreSQL's autovacuum maintains table health by removing dead tuples, updating statistics, and preventing transaction ID wraparound. This check identifies tables that may have vacuum-related issues due to:

- Disabled autovacuum settings
- Default configurations unsuitable for large tables
- Stale vacuum/analyze activity with real pending work

## Subchecks

### autovacuum-disabled

Lists tables where `autovacuum_enabled=false` has been explicitly set.

**Severity:** Warning

These tables rely entirely on manual maintenance. Common legitimate uses:
- Bulk import staging tables (re-enable after import)
- Tables managed by external ETL processes

### large-table-defaults

Identifies tables with more than 1 million rows using default autovacuum scale factors.

**Severity:** Warning

The default `autovacuum_vacuum_scale_factor` is 0.2 (20%), meaning autovacuum triggers when dead tuples exceed 20% of the table size:

| Table Size | Dead Tuples Before Vacuum |
|-----------|---------------------------|
| 1M rows   | 200,000 dead tuples       |
| 10M rows  | 2,000,000 dead tuples     |
| 100M rows | 20,000,000 dead tuples    |

**Recommended settings for large tables:**

```sql
ALTER TABLE schema.large_table SET (
  autovacuum_vacuum_scale_factor = 0.01,  -- 1% instead of 20%
  autovacuum_vacuum_threshold = 1000       -- Absolute minimum
);
```

### vacuum-stale

Identifies tables that haven't been vacuumed or analyzed recently despite pending work.

**Severity:**
- Warning: No vacuum/analyze in 7+ days with 250,000+ pending work
- Fail: No vacuum/analyze in 25+ days with 500,000+ pending work

Tables that go too long without maintenance may have:
- Outdated statistics leading to poor query plans
- Accumulated dead tuples causing bloat
- Increased disk usage from unreclaimed space

## Pending Work Column

The "Pending Work" column is the larger of:
- `n_dead_tup` + `n_ins_since_vacuum`: vacuum work (inserts count too, PostgreSQL 14+)
- `n_mod_since_analyze`: analyze work

## How to Fix

### For `autovacuum-disabled`

Tables with autovacuum disabled rely entirely on manual maintenance and may accumulate dead tuples, miss statistics updates, or risk transaction ID wraparound.

**Review each table and either:**

1. Re-enable autovacuum if the table should have automatic maintenance:
```sql
ALTER TABLE schema.table_name RESET (autovacuum_enabled);
```

2. Document the manual maintenance process if autovacuum should stay disabled (e.g., bulk import staging tables, externally managed tables)

**Monitor these tables regularly:**
```sql
SELECT relname, n_dead_tup, last_vacuum, last_autovacuum
FROM pg_stat_user_tables s
JOIN pg_class c ON c.oid = s.relid
WHERE c.reloptions @> ARRAY['autovacuum_enabled=false'];
```

### For `large-table-defaults`

Large tables using default autovacuum settings may vacuum too infrequently. The default `autovacuum_vacuum_scale_factor` is 0.2 (20%), meaning a 10M row table vacuums after 2M dead tuples.

**Recommended settings for large tables:**
```sql
ALTER TABLE schema.large_table SET (
  autovacuum_vacuum_scale_factor = 0.01,  -- 1% instead of 20%
  autovacuum_vacuum_threshold = 1000       -- Absolute minimum
);
```

**For very large tables (100M+ rows), consider even lower scale factors:**
```sql
ALTER TABLE schema.huge_table SET (
  autovacuum_vacuum_scale_factor = 0.001,  -- 0.1%
  autovacuum_vacuum_threshold = 5000
);
```

**Monitor vacuum frequency after changes:**
```sql
SELECT schemaname, relname, last_autovacuum, autovacuum_count
FROM pg_stat_user_tables
WHERE n_live_tup > 1000000
ORDER BY n_live_tup DESC;
```

### For `vacuum-stale`

Tables that haven't been vacuumed or analyzed recently may have outdated statistics, accumulated dead tuples, and increased disk usage.

**Immediate actions:**

1. Run VACUUM ANALYZE on affected tables:
```sql
VACUUM ANALYZE schema.table_name;
```

2. Check if autovacuum is running:
```sql
SELECT * FROM pg_stat_progress_vacuum;
```

3. Check for long-running transactions blocking vacuum:
```sql
SELECT pid, age(backend_xid), state, query
FROM pg_stat_activity
WHERE backend_xid IS NOT NULL
ORDER BY age(backend_xid) DESC;
```

**If autovacuum is not keeping up:**
- Increase `autovacuum_max_workers`
- Lower `autovacuum_vacuum_scale_factor` for busy tables
- Increase `autovacuum_vacuum_cost_limit`

For tables that are rarely updated, this may be expected behavior.

When the analyze arm is the one tripping, run `ANALYZE schema.table_name` (or lower `autovacuum_analyze_scale_factor` / `autovacuum_analyze_threshold` for busy tables) to refresh statistics.

## Prevention

1. Avoid disabling autovacuum unless absolutely necessary
2. Configure appropriate scale factors for tables >1M rows
3. Monitor vacuum activity with `pg_stat_user_tables`
4. Ensure autovacuum workers and cost limits are appropriately configured
5. Lower analyze thresholds for tables with high modification rates

## Related Checks

- `freeze-age`: Monitors transaction ID age at database and table level
- `vacuum-settings`: Validates global vacuum configuration
- `table-bloat`: Detects tables with excessive dead tuple bloat
- `statistics-freshness`: Validates database-level statistics maturity
