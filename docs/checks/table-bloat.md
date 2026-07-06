# Table Bloat Check

Monitors PostgreSQL tables for dead tuple accumulation indicating vacuum issues.

## What It Checks

### High Dead Tuples (`high-dead-tuples`)
Identifies tables with high dead tuple percentages. The finding reports **WARN**; the higher-threshold tables are listed first:
- Dead tuples > 40% of total (highest priority)
- Dead tuples > 20% of total

Dead tuples are rows marked for deletion but not yet reclaimed by vacuum.

### Stale Vacuum (`stale-vacuum`)
The single vacuum-freshness finding across the vacuum category. It gates on actual dead-tuple pressure combined with vacuum age (a time-only variant previously lived in `table-vacuum-health` and was retired because elapsed time alone barely correlated with bloat). Tables are listed worst-first (most dead tuples, then oldest vacuum):

- **WARN row:** dead tuples are >10% of the table **or** >100K in absolute terms, **and** the table has not been (auto)vacuumed in >3 days.
- **FAIL row:** >50K dead tuples **and** not (auto)vacuumed in >25 days (or never vacuumed).

The finding severity is derived from its worst row: it reports **WARN** when every row is WARN, and escalates to **FAIL** as soon as any table trips the FAIL row conditions.

### Large Bloated Tables (`large-bloated-tables`)
Identifies large tables where bloat wastes significant disk space. The finding reports **WARN**; the higher-threshold tables are listed first:
- Tables >10GB with >20% dead tuples (highest priority)
- Tables >1GB with >10% dead tuples

## Why This Matters

Table bloat causes:
- **Wasted disk space** - Dead tuples consume storage
- **Slower queries** - Sequential scans read dead tuples
- **Index bloat** - Indexes point to dead tuples
- **Transaction ID wraparound risk** - Old dead tuples prevent TXID cleanup

## How to Fix

### For `high-dead-tuples`

High dead tuple percentage indicates autovacuum is not keeping up with updates/deletes.

**Immediate fix:**
```sql
VACUUM ANALYZE schema.table_name;
```

**For extreme bloat (>50%), consider:**
```sql
VACUUM FULL schema.table_name;  -- Requires ACCESS EXCLUSIVE lock
```

**Prevent future bloat by tuning autovacuum:**
```sql
ALTER TABLE schema.table_name SET (
  autovacuum_vacuum_scale_factor = 0.1,    -- Vacuum at 10% dead (default 20%)
  autovacuum_vacuum_threshold = 1000,       -- Minimum dead tuples
  autovacuum_analyze_scale_factor = 0.05    -- Analyze at 5% change
);
```

**Also check:**
- `autovacuum_max_workers` (global setting)
- `maintenance_work_mem` (affects vacuum speed)

### For `stale-vacuum`

Tables with many dead tuples that haven't been vacuumed indicate autovacuum problems.

**Check if autovacuum is running:**
```sql
SELECT * FROM pg_stat_progress_vacuum;
```

**Check autovacuum configuration:**
```sql
SHOW autovacuum;
SHOW autovacuum_vacuum_scale_factor;
SHOW autovacuum_max_workers;
```

**Manual vacuum:**
```sql
VACUUM ANALYZE schema.table_name;
```

**Investigate why autovacuum isn't reaching these tables:**
- Long-running transactions holding back vacuum
- Too few `autovacuum_max_workers`
- Tables too large for available `maintenance_work_mem`

### For `large-bloated-tables`

Large tables with bloat waste significant disk space and slow down queries.

**For online cleanup (minimal locking):**
```sql
VACUUM ANALYZE schema.table_name;
```

**For maximum space reclamation (requires downtime):**
```sql
VACUUM FULL schema.table_name;  -- Rewrites entire table, ACCESS EXCLUSIVE lock
```

**Alternative: pg_repack (online, no locks):**
```bash
pg_repack -t schema.table_name
```

**Prevent future bloat:**
- Lower autovacuum thresholds for large tables
- Ensure adequate `maintenance_work_mem`
- Monitor for long-running transactions

## Related Checks

- `vacuum-settings` - Validates global autovacuum configuration
- `table-vacuum-health` - Monitors per-table autovacuum configuration
- `freeze-age` - Monitors transaction ID age
