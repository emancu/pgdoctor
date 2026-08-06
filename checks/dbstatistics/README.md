# DB Statistics

Reports how long the current database's cumulative statistics (`pg_stat_*` counters)
have been accumulating, and warns when that window is too short for usage-based
analysis.

This is about **counter age**, not planner statistics. `ANALYZE` freshness is covered
by `table-vacuum-health`.

## What It Checks

### Statistics Age

The window since `pg_stat_reset()` was last called for this database. The age is
reported in the check's title, so it is visible without expanding the finding.

**Thresholds**:
- **OK**: Counters cover ≥ 7 days
- **WARN**: Counters cover < 7 days

## Why Statistics Age Matters

Many pgdoctor checks measure over this window:

- **index-usage**: Requires accumulated index scan counts to identify unused indexes
- **table-seq-scans**: Needs sequential vs index scan ratios over time
- **cache-efficiency**: Uses cumulative cache hit/miss data
- **temp-usage**: Divides temp file totals by the window to produce a per-hour rate

The `pg_stat_statements` counters are a **separate, independent clock** with its own
reset function — see the `query-statistics` check.

### What Resets Counters

1. **Unclean shutdown, crash, or OOM kill** - counters are zeroed, and `stats_reset`
   stays NULL. This is the case that looks like "never reset" but is not.
2. **A freshly built replica, restore, or clone** - counters start from that node's
   own history, also with a NULL `stats_reset`.
3. **Manual `pg_stat_reset()` calls** - the only case that records a timestamp.
4. **Major version upgrades** - cumulative counters do not carry over.

A **clean restart does not reset them**. Since PostgreSQL 15 the statistics are
written to disk at shutdown and reloaded on start; only an unclean start discards
them.

Because of (1) and (2), a NULL `stats_reset` is reported as "never reset" without any
claim about how much history the counters hold.

### Never Run `pg_stat_reset()` To "Fix" This

Resetting also zeroes `n_dead_tup` and `n_mod_since_analyze` for every table, which
are exactly the counters autovacuum uses to decide what to work on. On a busy primary
that stalls vacuum scheduling until the thresholds are crossed again from zero. Wait
for the window to mature instead.

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
- After a failover, promotion, or crash recovery
- After running `pg_stat_reset()` for troubleshooting
- Post-migration or major schema changes

## How to Fix

### For `db-statistics`

Statistics-based checks require at least 7 days of accumulated data to reflect typical workload patterns.

**Recommendations:**

1. **Wait for maturity**: Rerun pgdoctor after 7+ days
2. **Check for a recent failover or crash**: either starts the counters over
3. **Review reset calls**: Check logs for manual `pg_stat_reset()` calls
4. **Consider context**: New databases need time to accumulate statistics

**Checking statistics age:**
```sql
-- Current statistics age, and how long the server has been up.
-- A NULL stats_reset with a short uptime means the counters may be young
-- even though no reset was recorded.
SELECT
  datname,
  stats_reset,
  now() - stats_reset AS age,
  pg_postmaster_start_time() AS postmaster_start
FROM pg_stat_database
WHERE datname = current_database();
```

**Affected checks rely on:**
- `pg_stat_user_indexes` (index scan counts)
- `pg_stat_user_tables` (table scan patterns)
- `pg_stat_database` (cache hit ratios)

These counters accumulate over time and start over on a manual `pg_stat_reset()`, an
unclean shutdown, or a node rebuild.

## Query Details

Queries `pg_stat_database` for the statistics reset timestamp of the current database.
The reported age is derived from that timestamp rather than a whole-day count, so a
recent reset reads as `45m` rather than `0 days`.
