# Cache Efficiency

Analyzes database-wide buffer cache hit ratio to identify memory pressure and I/O bottlenecks.

> **Note**: This check depends on PostgreSQL runtime statistics. For accurate results, statistics should be at least 7 days old. Run the `statistics-freshness` check to validate statistics maturity.

## What It Checks

### Database Cache Hit Ratio

Calculates the percentage of data blocks read from memory (buffer cache) vs. disk.

**Formula**: `blocks_hit / (blocks_hit + blocks_read) * 100`

**Thresholds**:
- **FAIL**: < 90% cache hit ratio
- **WARN**: < 95% cache hit ratio
- **OK**: ≥ 95% cache hit ratio

## Why Cache Hit Ratio Matters

### Performance Impact

**Low cache hit ratios indicate**:
- Frequent disk I/O (100-1000x slower than memory)
- Insufficient `shared_buffers` configuration
- Working set larger than available memory
- Potential query inefficiencies

**High cache hit ratios indicate**:
- Most data accessed from memory (fast)
- Appropriate buffer cache sizing
- Efficient query patterns

### Typical Ratios

| Ratio | Interpretation |
|-------|----------------|
| > 99% | Excellent - working set fits in memory |
| 95-99% | Good - most data cached, occasional disk reads |
| 90-95% | Concerning - significant disk I/O |
| < 90% | Critical - high disk I/O, performance degraded |

## Statistics Requirements

This check requires at least **7 days** of statistics history. Recent statistics resets will trigger a warning.

## How to Fix

### For `cache-hit-ratio`

**Increase shared_buffers**

**AWS RDS**:
```
# In parameter group
shared_buffers = {DBInstanceClassMemory/32768}  # 25% of RAM
```

**Self-hosted PostgreSQL**:
```
# In postgresql.conf
shared_buffers = 8GB  # Adjust based on available RAM
```

**Guidelines**:
- Dedicated database server: 25% of total RAM
- Shared server: 10-15% of total RAM
- Must restart PostgreSQL after changing

**Identify Inefficient Queries**

```sql
-- Find queries with high disk reads
SELECT query, shared_blks_hit, shared_blks_read,
       shared_blks_hit::float / NULLIF(shared_blks_hit + shared_blks_read, 0) AS cache_ratio
FROM pg_stat_statements
WHERE shared_blks_read > 1000
ORDER BY shared_blks_read DESC
LIMIT 20;
```

**Optimize Query Patterns**

- Add missing indexes (see `table-seq-scans` and `foreign-key-indexes` checks)
- Reduce data scanned with better WHERE clauses
- Use covering indexes to avoid table lookups
- Partition large tables to reduce working set

**Reduce Table Bloat**

```sql
-- Check table bloat
SELECT schemaname, tablename,
       pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
FROM pg_tables
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;

-- Reclaim space
VACUUM FULL tablename;  -- Requires exclusive lock
-- OR
pg_repack tablename;    -- Online alternative
```

**Architecture Changes**

For persistent low cache ratios despite tuning:
- **Read replicas**: Distribute read load
- **Partitioning**: Reduce working set size per query
- **Archival**: Move old data to separate storage
- **Caching layer**: Add application-level cache (Redis, Memcached)

## False Positives

Low cache ratios may be acceptable for:
- **Data warehouses**: Large sequential scans are normal
- **ETL workloads**: Batch processing scans large data sets
- **Recently started databases**: Cache warming in progress

Run this check during normal OLTP workload periods for accurate results.

## Query Details

Queries `pg_stat_database` for the current database's block hit and read counters, calculating the cache hit percentage.
