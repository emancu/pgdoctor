# Index Usage

Analyzes index usage patterns to identify unused and inefficient indexes that waste disk space and slow down write operations.

> **Note**: This check depends on PostgreSQL runtime statistics. For accurate results, statistics should be at least 7 days old. Run the `statistics-freshness` check to validate statistics maturity.

## What It Checks

### 1. Unused Indexes
Indexes with zero scans that are larger than 10 MB. These indexes consume disk space and add overhead to INSERT/UPDATE/DELETE operations without providing query benefits.

**Severity**: WARN

**Excludes**:
- Primary keys (required for constraints)
- Unique indexes (enforce data integrity)

### 2. Low Usage Indexes
Indexes with fewer than 1,000 scans but more than 10,000 table writes. These indexes have high maintenance costs relative to their query benefits.

**Severity**: WARN

## Statistics Requirements

This check requires at least **7 days** of statistics history for accurate results. If statistics were recently reset (PostgreSQL restart, manual reset), the check will warn about insufficient data.

## Important Considerations

### Master/Replica Statistics
Statistics are collected per-instance. An index unused on the primary may be heavily used on read replicas for reporting queries.

**Recommendation**: Run this check on ALL instances (primary and all replicas) before dropping indexes.

### Statistics Reset
PostgreSQL resets statistics on restart or manual `pg_stat_reset()`. Check the statistics age before making decisions.

### Seasonal Usage
Some indexes may appear unused but are critical for:
- End-of-month/quarter reports
- Annual batch processes
- Rarely-run admin queries

Review index purpose before dropping.

## How to Fix

### For `unused-indexes`

Unused indexes waste disk space and slow down writes.

**IMPORTANT**: `idx_scan` is per-instance and resets on failover/restart. Verify usage on EVERY replica (and check `pg_stat_database.stats_reset`) before dropping. An index unused on the primary may be critical for read-replica queries.

```sql
-- Run on the primary AND every replica; confirm the counter is meaningful.
SELECT idx_scan FROM pg_stat_user_indexes WHERE indexrelname = 'index_name';
SELECT stats_reset FROM pg_stat_database WHERE datname = current_database();

-- Drop only once idle everywhere.
DROP INDEX CONCURRENTLY schema.index_name;
```

**Before dropping:**
1. Verify the index is idle on the primary and on every read replica
2. Check `pg_stat_database.stats_reset` so you are not reading a freshly-reset counter
3. Check application code for references
4. Consider creating the index conditionally in migrations for rollback safety
5. Monitor query performance after dropping

### For `low-usage-indexes`

These indexes are rarely used for queries but maintained on every write.

**IMPORTANT**: `idx_scan` is per-instance and resets on failover/restart. Verify usage on EVERY replica (and check `pg_stat_database.stats_reset`) before dropping — an index barely read on the primary may be hot on a replica.

Consider if these indexes are:
1. For rarely-run reports (keep)
2. Truly unused (drop)
3. Needed on read replicas (keep)

Evaluate index value vs maintenance cost for your workload.

## Query Details

Queries `pg_stat_user_indexes` and `pg_stat_user_tables` for usage analysis.
