# Table Activity Check

Analyzes table write activity to identify high-churn tables and HOT update efficiency issues.

> **Note**: This check depends on PostgreSQL runtime statistics. For accurate results, statistics should be at least 7 days old. Run the `statistics-freshness` check to validate statistics maturity.

## What It Checks

### High Churn Tables (`high-churn-tables`)
Identifies tables with excessive write activity since statistics were last reset:
- **WARN**: > 1 million total writes (inserts + updates + deletes)

High-churn tables may need more aggressive autovacuum settings or architectural review.

### Low HOT Ratio (`low-hot-ratio`)
Identifies tables with poor Heap-Only Tuple (HOT) update efficiency:
- **WARN**: < 50% HOT ratio on tables with > 1 million rows

HOT updates are an optimization where PostgreSQL can update a row in place without updating indexes. Low HOT ratios indicate potential performance issues.

## How It Works

This check queries `pg_stat_user_tables` to analyze:
- **Total writes**: `n_tup_ins + n_tup_upd + n_tup_del`
- **HOT ratio**: `n_tup_hot_upd / n_tup_upd * 100` (percentage of updates that were HOT)
- **Row count**: `n_live_tup` (approximate live rows)

## Why It Matters

### High Churn Impact
- Tables with heavy write activity accumulate dead tuples faster
- May overwhelm default autovacuum settings
- Can cause bloat if vacuum doesn't keep up
- Indicates potential hotspots in application design

### Low HOT Impact
- Non-HOT updates require index updates for every row change
- Increases WAL volume and replication lag
- Causes faster index bloat
- Slows down UPDATE operations

## How to Fix

### For `high-churn-tables`

1. **Tune autovacuum for specific tables:**
```sql
-- More aggressive vacuum for high-churn table
ALTER TABLE your_table SET (
  autovacuum_vacuum_scale_factor = 0.05,     -- Vacuum at 5% dead tuples (default 20%)
  autovacuum_vacuum_cost_delay = 2,          -- Less sleep between vacuum operations
  autovacuum_vacuum_cost_limit = 1000        -- Higher work limit per round
);
```

2. **Review application patterns:**
   - Consider batch operations instead of row-by-row updates
   - Use UPSERT (`INSERT ... ON CONFLICT`) where appropriate
   - Archive or delete old data more frequently

### For `low-hot-ratio`

#### Understanding HOT Updates and FILLFACTOR

**HOT (Heap-Only Tuple) updates** allow PostgreSQL to update a row without modifying indexes, which is much faster. However, HOT updates only work when:
1. No indexed column is modified
2. There's free space on the same page to store the new row version

**FILLFACTOR** controls how much of each page PostgreSQL fills during INSERTs. The default is 100% (fill completely). Setting it lower (e.g., 90%) leaves 10% free space on each page for future updates to use for HOT.

> **Docs**: [PostgreSQL FILLFACTOR](https://www.postgresql.org/docs/current/sql-createtable.html#SQL-CREATETABLE-STORAGE-PARAMETERS)

#### Remediation Options

**1. Review which columns are being updated (quick win):**

HOT updates fail if any indexed column changes. Common culprits:
- `updated_at` timestamp columns with indexes
- `status` or `state` columns with indexes
- Any column that changes frequently AND has an index

```sql
-- Find indexes on the problematic table
SELECT indexname, indexdef
FROM pg_indexes
WHERE tablename = 'your_table';

-- Consider: Do you really need an index on that frequently-updated column?
-- Partial indexes can help: only index rows that matter for queries
CREATE INDEX CONCURRENTLY idx_orders_status_pending
  ON orders (status) WHERE status = 'pending';
```

**2. Adjust FILLFACTOR (requires table rebuild):**

Setting a lower FILLFACTOR leaves room for HOT updates:
```sql
-- Set fillfactor to 90% (leaves 10% for updates)
ALTER TABLE your_table SET (fillfactor = 90);
```

**Important**: This only affects *new* pages. Existing pages keep their current fill level. To apply to existing data, the table needs to be rebuilt, which requires **DBA assistance** (involves either `VACUUM FULL` which locks the table, or `pg_repack` which requires elevated permissions).

**3. Contact DBA/Data Engineering:**

If the above options don't resolve the issue, contact the DBA or Data Engineering team. They can:
- Run `pg_repack` to rebuild the table with minimal locking
- Evaluate if the table structure needs changes
- Help with index strategy review

## Thresholds Rationale

| Subcheck | Threshold | Rationale |
|----------|-----------|-----------|
| high-churn | > 1M writes | Significant activity that may need tuning |
| low-hot-ratio | < 50% on > 1M rows | Below this, you're losing half the HOT benefit |

## Related Checks

- `table-bloat` - Dead tuple accumulation (often caused by high churn)
- `table-vacuum-health` - Vacuum running properly
- `freeze-age` - Transaction ID wraparound risk
- `partitioning` - Validates partition setup for large tables
