# Table Bloat Check

Monitors PostgreSQL tables for dead tuple accumulation and the disk space it wastes.

## What It Checks

### High Dead Tuples (`high-dead-tuples`)
Identifies tables with high dead tuple percentages:
- **WARN**: Dead tuples > 20% of total

Dead tuples are rows marked for deletion but not yet reclaimed by vacuum. A high
ratio degrades performance (bloated scans, index bloat) but never stops the
database, so this finding stays WARN and never pages — vacuum emergencies surface
via the `table-vacuum-health` check.

### Large Bloated Tables (`large-bloated-tables`)
Identifies large tables where bloat wastes significant disk space:
- **FAIL**: Tables >10GB with >20% dead tuples
- **WARN**: Tables >1GB with >10% dead tuples

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
