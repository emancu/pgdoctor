# Statistics Freshness

Validates that PostgreSQL runtime statistics are mature enough for accurate usage-based analysis.

## What It Checks

### Statistics Age

Checks how long ago `pg_stat_reset()` was called or PostgreSQL was restarted.

**Thresholds**:
- **OK**: Statistics are ≥ 7 days old
- **WARN**: Statistics are < 7 days old

## Why Statistics Age Matters

Many pgdoctor checks rely on PostgreSQL's runtime statistics to make recommendations:

- **index-usage**: Requires accumulated index scan counts to identify unused indexes
- **table-seq-scans**: Needs sequential vs index scan ratios over time
- **cache-efficiency**: Uses cumulative cache hit/miss data

### Statistics Reset Events

Statistics are reset when:
1. **PostgreSQL restarts** - Most common cause
2. **Manual `pg_stat_reset()` calls** - Intentional reset
3. **Major version upgrades** - Statistics don't carry over

### Why 7 Days?

- **Weekly patterns**: Many workloads have weekly cycles (end-of-week reports, batch jobs)
- **Outlier smoothing**: Short-term spikes or drops get averaged out
- **Confidence**: 7+ days provides representative data for most OLTP workloads

## Impact of Fresh Statistics

### False Positives

Fresh statistics can cause:
- **Unused indexes**: Recently created indexes appear unused
- **High seq scans**: Tables without workload history flagged incorrectly
- **Low cache ratios**: Cache warming period not complete

### When Fresh Statistics Are Expected

- New database instances
- Recent PostgreSQL restarts (deployment, failover)
- After running `pg_stat_reset()` for troubleshooting
- Post-migration or major schema changes

## How to Fix

### For `statistics-freshness`

Statistics-based checks require at least 7 days of accumulated data to reflect typical workload patterns.

**Recommendations:**

1. **Wait for maturity**: Rerun pgdoctor after 7+ days
2. **Check restart history**: Verify if PostgreSQL restarted recently
3. **Review reset calls**: Check logs for manual `pg_stat_reset()` calls
4. **Consider context**: New databases need time to accumulate statistics

**Checking statistics age:**
```sql
-- Current statistics age
SELECT
  datname,
  stats_reset,
  now() - stats_reset AS age
FROM pg_stat_database
WHERE datname = current_database();

-- Check for recent restarts (requires pg_stat_bgwriter)
SELECT
  stats_reset AS bgwriter_reset
FROM pg_stat_bgwriter;
```

**Affected checks rely on:**
- `pg_stat_user_indexes` (index scan counts)
- `pg_stat_user_tables` (table scan patterns)
- `pg_stat_database` (cache hit ratios)

These statistics accumulate over time and are reset on PostgreSQL restarts or manual `pg_stat_reset()` calls.

## Query Details

Queries `pg_stat_database` for the statistics reset timestamp and calculates age in days.
