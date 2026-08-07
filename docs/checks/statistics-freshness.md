# Statistics Freshness

Validates that PostgreSQL runtime statistics are mature enough for accurate usage-based analysis.

## What It Checks

### Statistics Age

Checks how long ago `pg_stat_reset()` was called or PostgreSQL was restarted.

**Thresholds**:
- **OK**: Counters reach back ≥ 7 days
- **WARN**: Counters reach back < 7 days, or may not

Only `pg_stat_reset()` records a timestamp. A crash, unclean shutdown, or rebuilt
replica zeroes the counters and leaves `stats_reset` NULL, so the absence of a reset
is not evidence the counters are old. With no reset recorded the window is measured
from server start instead, which is a lower bound: a clean restart preserves the
counters (PG15+), and everything that zeroes them coincides with a start.

That means a recently restarted server reports WARN even with no reset recorded —
which is the point. It is the state where `index-usage` will report a busy index as
having zero scans.

## Why Statistics Age Matters

Many pgdoctor checks rely on PostgreSQL's runtime statistics to make recommendations:

- **index-usage**: Requires accumulated index scan counts to identify unused indexes
- **table-seq-scans**: Needs sequential vs index scan ratios over time
- **cache-efficiency**: Uses cumulative cache hit/miss data
- **temp-usage**: Divides temp file totals by the window to get a per-hour rate

### Statistics Reset Events

Statistics are reset when:
1. **Unclean shutdown or crash** - counters are zeroed and `stats_reset` stays NULL
2. **A rebuilt replica, restore, or clone** - same, counters start from that node's own history
3. **Manual `pg_stat_reset()` calls** - the only cause that records a timestamp
4. **Major version upgrades** - Statistics don't carry over

A clean restart does **not** reset them: since PostgreSQL 15 the statistics are
written to disk at shutdown and reloaded on start.

Because the first two leave no timestamp, a NULL `stats_reset` is reported as "never
reset" without any claim about how much history the counters hold.

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
2. **Check for a recent failover or crash**: either starts the counters over
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
```

**Affected checks rely on:**
- `pg_stat_user_indexes` (index scan counts)
- `pg_stat_user_tables` (table scan patterns)
- `pg_stat_database` (cache hit ratios)

These statistics accumulate over time and start over on a manual `pg_stat_reset()`, an
unclean shutdown, or a node rebuild.

## Query Details

Queries `pg_stat_database` for the statistics reset timestamp. The age is computed by
the server, so it is not skewed by the clock of the host running pgdoctor, and is not
truncated to whole days: a recent reset reads `45m` rather than `0 days`. It is
reported in the check's title, because renderers drop a PASS finding's details.
