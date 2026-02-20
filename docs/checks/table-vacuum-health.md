# Table Vacuum Health Check

Monitors per-table autovacuum configuration and activity to identify maintenance issues.

## Background

PostgreSQL's autovacuum maintains table health by removing dead tuples, updating statistics, and preventing transaction ID wraparound. This check identifies tables that may have vacuum-related issues due to:

- Disabled autovacuum settings
- Default configurations unsuitable for large tables
- Stale vacuum/analyze activity
- Excessive modifications without ANALYZE

## Subchecks

### autovacuum-disabled

Lists tables where `autovacuum_enabled=false` has been explicitly set.

**Severity:** Warning

These tables rely entirely on manual maintenance. Common legitimate uses:
- Bulk import staging tables (re-enable after import)
- Tables managed by external ETL processes

### large-table-defaults

Identifies tables with more than 1 million rows using default autovacuum scale factors.

**Severity:**
- Warning: Tables with 1M-10M rows
- Fail: Tables with >10M rows

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

Identifies tables that haven't been vacuumed or analyzed recently.

**Severity:**
- Warning: No vacuum/analyze in 7+ days
- Fail: No vacuum/analyze in 25+ days

Tables that go too long without maintenance may have:
- Outdated statistics leading to poor query plans
- Accumulated dead tuples causing bloat
- Increased disk usage from unreclaimed space

### analyze-needed

Identifies tables with many modifications since the last ANALYZE, indicating stale statistics.

**Severity:**
- Warning: 100,000+ modifications since last analyze
- Fail: 500,000+ modifications since last analyze

Stale statistics can cause:
- Poor query plans (wrong join orders, missing index usage)
- Inaccurate row estimates leading to memory issues
- Suboptimal parallel query decisions

This check differs from `statistics-freshness` which validates **database-level** stats age. This subcheck identifies **per-table** stats staleness based on actual modification activity.

## Pending Work Column

The "Pending Work" column shown in some subchecks combines:
- `n_dead_tup`: Dead tuples from updates/deletes
- `n_ins_since_vacuum`: Inserted rows since last vacuum (PostgreSQL 14+)

This gives a fuller picture of how much work vacuum needs to do on each table.

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

### For `analyze-needed`

Tables with many modifications since the last ANALYZE have stale statistics, which can cause poor query plans, inaccurate row estimates, and suboptimal parallel query decisions.

**Immediate actions:**

1. Run ANALYZE on affected tables:
```sql
ANALYZE schema.table_name;
```

2. Check if autoanalyze is keeping up:
```sql
SELECT schemaname, relname, n_mod_since_analyze, last_autoanalyze, autoanalyze_count
FROM pg_stat_user_tables
WHERE n_mod_since_analyze > 100000
ORDER BY n_mod_since_analyze DESC;
```

3. Consider lowering analyze thresholds for busy tables:
```sql
ALTER TABLE schema.busy_table SET (
  autovacuum_analyze_scale_factor = 0.02,  -- 2% instead of 10%
  autovacuum_analyze_threshold = 1000
);
```

**PostgreSQL's default autoanalyze triggers when:**
```
modified_rows > autovacuum_analyze_threshold + (autovacuum_analyze_scale_factor * table_rows)
Default: modified > 50 + (0.1 * rows) = 10% of table + 50 rows
```

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
