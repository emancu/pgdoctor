# Temporary File Usage Check

Monitors PostgreSQL temporary file creation which indicates queries spilling to disk.

> **Note**: Thresholds are tuned for production-scale databases. This check acts as a **regression detector** rather than an absolute health check - it catches significant increases from baseline that indicate query plan regressions, new inefficient queries, or work_mem configuration issues.

## What It Checks

### Temp File Creation Rate (`temp-file-rate`)
Monitors the rate of temporary file creation:
- **FAIL**: ≥20 files/hour (indicates serious regression or multiple problematic queries)
- **WARN**: ≥5 files/hour (indicates new inefficient queries or query plan changes)
- **Baseline**: Well-tuned production databases typically see <1 file/hour

### Temp Data Volume Rate (`temp-volume-rate`)
Monitors the volume of temp data written:
- **FAIL**: ≥5 GB/hour (major regression or multiple large queries spilling to disk)
- **WARN**: ≥1 GB/hour (increased large sorts/hashes from new features or query changes)
- **Baseline**: Well-tuned production databases typically see 100-200MB/hour

## Why This Matters

Temporary files are created when PostgreSQL operations exceed `work_mem`:
- **Sorts** - ORDER BY, DISTINCT, merge joins
- **Hash tables** - Hash joins, hash aggregates
- **Bitmap heaps** - Index scans exceeding work_mem

Temp files cause:
- **Slow queries** - Disk I/O is 100x slower than memory
- **Disk pressure** - Competes with WAL, tables, indexes
- **Unpredictable performance** - Depends on disk availability

## How to Fix

### For `temp-file-rate`

High temp file creation rate (>5 files/hour) indicates queries spilling to disk. Fix by increasing work_mem or optimizing queries:

**Option 1: Increase work_mem globally (use with caution)**
```sql
-- Calculate safe value: (Available RAM * 0.25) / max_connections
-- Example for 32GB RAM, 200 connections: (32 * 0.25) / 200 = 40MB
ALTER SYSTEM SET work_mem = '40MB';
SELECT pg_reload_conf();
```

**Option 2: Set work_mem per-role (safer)**
```sql
-- Give analytics/reporting users more memory
ALTER ROLE analytics_user SET work_mem = '256MB';

-- Keep application users at default
ALTER ROLE app_user SET work_mem = '16MB';
```

**Option 3: Optimize problematic queries**
```sql
-- Enable logging to identify queries creating temp files
ALTER SYSTEM SET log_temp_files = 10240;  -- Log temp files >10MB
SELECT pg_reload_conf();

-- Query pg_stat_statements to find offenders
SELECT query, calls, temp_blks_written,
       pg_size_pretty(temp_blks_written * 8192) AS temp_size
FROM pg_stat_statements
WHERE temp_blks_written > 0
ORDER BY temp_blks_written DESC
LIMIT 20;

-- Then optimize queries: add indexes, rewrite joins, limit result sets
```

### For `temp-volume-rate`

High temp data volume (>1GB/hour) indicates large sorts/hashes spilling to disk:

**Option 1: Increase work_mem (same as temp-file-rate)**

**Option 2: Optimize large queries**
```sql
-- Find queries writing large temp files
SELECT query, calls, temp_blks_written,
       pg_size_pretty(temp_blks_written * 8192) AS temp_size
FROM pg_stat_statements
WHERE temp_blks_written > 0
ORDER BY temp_blks_written DESC
LIMIT 10;

-- Common optimizations:
-- 1. Add indexes to reduce sort/hash operations
-- 2. Use LIMIT/pagination for large result sets
-- 3. Avoid ORDER BY on large datasets when not needed
-- 4. Use partial indexes for filtered queries
-- 5. Consider materialized views for expensive aggregations
```

**Option 3: Use maintenance_work_mem for maintenance operations**
```sql
-- If VACUUM/REINDEX causing temp files, increase maintenance_work_mem
ALTER SYSTEM SET maintenance_work_mem = '1GB';
SELECT pg_reload_conf();
```

## work_mem Tuning

`work_mem` is allocated **per operation**, not per connection. A single complex query can use many work_mem allocations.

### Safe Formula for RDS
```
work_mem = (Available RAM * 0.25) / max_connections
```

Example for db.r5.xlarge (32GB RAM, 200 connections):
```sql
-- (32GB * 0.25) / 200 = 40MB
ALTER SYSTEM SET work_mem = '40MB';
SELECT pg_reload_conf();
```

### Per-Role Settings (Safer)
```sql
ALTER ROLE analytics_user SET work_mem = '256MB';
ALTER ROLE app_user SET work_mem = '16MB';
```

## Debugging Temp Files

### Enable Logging
```sql
-- Log all temp files >10MB
ALTER SYSTEM SET log_temp_files = 10240;
SELECT pg_reload_conf();
```

### Query pg_stat_statements
```sql
SELECT query, calls, temp_blks_written,
       pg_size_pretty(temp_blks_written * 8192) AS temp_size
FROM pg_stat_statements
WHERE temp_blks_written > 0
ORDER BY temp_blks_written DESC
LIMIT 20;
```

## Related Checks

- `session-settings` - Validates role-level work_mem settings
- `vacuum-settings` - Related memory settings (maintenance_work_mem)
