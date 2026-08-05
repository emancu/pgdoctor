# Index Usage

Analyzes index usage patterns to identify unused and inefficient indexes that waste disk space and slow down write operations.

> **Note**: This check depends on PostgreSQL runtime statistics. For accurate results, statistics should be at least 7 days old. Run the `statistics-freshness` check to validate statistics maturity.

## What It Checks

### 1. Unused Indexes
Indexes with zero scans that are larger than 500 MB. These indexes consume disk space and add overhead to
INSERT/UPDATE/DELETE operations without providing query benefits.
Details disclose the statistics window (since `pg_stat_database.stats_reset`) so "0 scans" is interpretable.

**Severity**: WARN

**Excludes**:
- Primary keys (required for constraints)
- Unique indexes (enforce data integrity)

### 2. Low Usage Indexes
Indexes larger than 500 MB with more than 10,000 table writes and at least one scan but a sustained rate
below 1 per week, over a statistics window of at least 30 days. Zero-scan indexes surface as unused-indexes.
These indexes have high maintenance costs relative to their query benefits.

**Severity**: INFO

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

**IMPORTANT**: Check usage on ALL instances (primary + replicas) before dropping! An index unused on primary may be critical for read replica queries.

```sql
-- Verify usage on all instances
SELECT idx_scan FROM pg_stat_user_indexes WHERE indexrelname = 'index_name';

-- Drop unused index
DROP INDEX CONCURRENTLY schema.index_name;
```

**Before dropping:**
1. Verify the index isn't used on read replicas
2. Check application code for references
3. Consider creating the index conditionally in migrations for rollback safety
4. Monitor query performance after dropping

### For `low-usage-indexes`

These indexes are rarely used for queries but maintained on every write.

Consider if these indexes are:
1. For rarely-run reports (keep)
2. Truly unused (drop)
3. Needed on read replicas (keep)

Evaluate index value vs maintenance cost for your workload.

## Query Details

Queries `pg_stat_user_indexes`, `pg_stat_user_tables`, and `pg_stat_database` for comprehensive usage analysis.
