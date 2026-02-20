# Table Sequential Scans

Identifies tables with excessive sequential scan activity relative to index scans, indicating potential missing indexes.

> **Note**: This check depends on PostgreSQL runtime statistics. For accurate results, statistics should be at least 7 days old. Run the `statistics-freshness` check to validate statistics maturity.

## What It Checks

### High Sequential Scan Ratios

Analyzes the ratio of sequential scans to index scans on tables:

**FAIL**:
- Tables with > 50,000 rows
- Sequential scan / index scan ratio > 50:1
- Has at least one index (tables without indexes are excluded)

**WARN**:
- Tables with > 10,000 rows
- Sequential scan / index scan ratio > 10:1
- Has at least one index

**Excludes**:
- Small tables (< 10,000 rows) where sequential scans are efficient
- Tables with no indexes (may be intentional staging/temp tables)
- System schemas

## Statistics Requirements

This check requires at least **7 days** of statistics history. Recent statistics resets will trigger a warning.

## Important Considerations

### When Sequential Scans Are Intentional

High sequential scan ratios may be expected for:

1. **OLAP Workloads**: Analytics queries that process large portions of tables
2. **Batch Processing**: ETL jobs that legitimately scan entire tables
3. **Small Dimension Tables**: Tables small enough that sequential scans are faster than index lookups
4. **Report Tables**: Tables used primarily for full-table aggregations

### False Positives

This check can produce false positives for:
- Data warehouse fact tables (sequential scans are normal)
- Tables with very low selectivity queries
- Recently created tables without representative workload

Always analyze actual query patterns before adding indexes.

## How to Fix

### Investigation Steps

1. **Identify Problematic Queries**:
```sql
-- Check pg_stat_statements for queries on this table
SELECT query, calls, total_exec_time, mean_exec_time
FROM pg_stat_statements
WHERE query LIKE '%table_name%'
ORDER BY total_exec_time DESC;
```

2. **Analyze Query Plans**:
```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM table_name WHERE commonly_filtered_column = value;
```

3. **Check Current Indexes**:
```sql
SELECT indexname, indexdef
FROM pg_indexes
WHERE tablename = 'table_name';
```

### Creating Indexes

```sql
-- Always use CONCURRENTLY to avoid blocking writes
CREATE INDEX CONCURRENTLY idx_table_column
ON schema.table_name(column_name);

-- For multi-column indexes, put most selective column first
CREATE INDEX CONCURRENTLY idx_table_col1_col2
ON schema.table_name(col1, col2);
```

### When NOT to Add Indexes

Avoid adding indexes if:
- Queries legitimately need to scan most of the table
- Table is small (< 10,000 rows)
- Workload is primarily INSERT/UPDATE heavy (indexes slow writes)
- Column has low cardinality (few distinct values)

## Query Details

Queries `pg_stat_user_tables` and `pg_class` to compare sequential scan and index scan activity, filtering for tables with significant row counts.
