# Index Bloat Check

Bloated indexes waste disk space and slow down queries.

## What It Checks

Estimates B-tree index bloat using page layout math and reports a single
finding. An index is listed when:

- Bloat >= 50% **and** index size >= 200 MiB, **or**
- Wasted space >= 2 GiB

The size floor drops small indexes: a sub-200 MiB index at 70% bloat wastes
too little to be worth a REINDEX. Any index wasting >= 2 GiB is always listed.

Listed indexes are tiered by row severity:

- **WARN**: wasted space >= 2 GiB or bloat >= 70%
- **INFO**: bloat between 50% and 70% with less than 2 GiB wasted (small-waste tail)

The finding itself is at most WARN — index bloat is a chronic maintenance
issue, never an urgent outage signal.

## How It Works

This check estimates bloat mathematically without requiring the `pgstattuple` extension:

1. **Calculate expected index size** based on:
   - Number of tuples (`reltuples`)
   - Sum of `avg_width` from `pg_stats` for indexed columns
   - Page layout constants (block_size=8192, headers=40 bytes)
   - Fillfactor setting (default 90%)

2. **Compare to actual size** (`relpages`)

3. **Bloat % = (actual - expected) / actual × 100**

**Accuracy**: ±15% - good enough for health checks, not precision measurement.
Uses a simplified 20% padding estimate instead of per-column MAXALIGN calculation.

## Why Index Bloat Happens

Index bloat accumulates when:
- Rows are deleted (index entries marked dead, not removed)
- Rows are updated (old index entry dead, new entry added)
- Heavy UPDATE/DELETE workloads without maintenance

Unlike table bloat, **VACUUM does NOT reclaim index space** - only REINDEX does.

## Impact

Bloated indexes cause:
- **Wasted disk space** - Dead entries consume storage
- **Slower queries** - More pages to scan
- **Poor cache efficiency** - Useful data diluted by dead entries
- **Longer backups** - More data to copy

## How to Fix

Rebuild bloated indexes, largest wasted space first:

```sql
-- Rebuild single index (no locks)
REINDEX INDEX CONCURRENTLY schema.index_name;

-- Rebuild all indexes on a table
REINDEX TABLE CONCURRENTLY schema.table_name;
```

`REINDEX CONCURRENTLY` builds a new index without blocking writes, but
temporarily requires additional disk space. For very large indexes, schedule
during low-traffic periods.

## Estimation Accuracy

This check uses the same mathematical approach as:
- check_postgres monitoring tool
- pgexperts diagnostic scripts
- Various PostgreSQL monitoring solutions

**Limitations:**
- Only estimates B-tree indexes (most common type)
- Accuracy depends on up-to-date `pg_stats` (run ANALYZE)
- May over-estimate for indexes with many NULLs
- Skips indexes < 100 pages (~800KB) to avoid noise

For precise measurement, use `pgstattuple` extension:
```sql
SELECT * FROM pgstattuple('index_name');
```

## Prevention

1. **Regular maintenance** - Schedule periodic REINDEX
2. **Monitor growth** - Track index size over time
3. **Tune autovacuum** - More aggressive settings reduce dead tuples faster
4. **Consider partitioning** - Smaller partitions = easier maintenance

## Related Checks

- `invalid-indexes` - Indexes in invalid state
- `table-bloat` - Dead tuples in tables (different from index bloat)
