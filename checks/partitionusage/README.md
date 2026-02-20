# Partition Key Usage Check

Detects queries on partitioned tables that don't use partition keys in their WHERE clause, causing full scans across all partitions.

## Requirements

- **pg_stat_statements extension** must be installed and enabled for full query pattern analysis
- PostgreSQL 15+

If `pg_stat_statements` is not installed, this check will report a WARNING and skip query pattern analysis. The sequential scan analysis will still run as it uses `pg_stat_user_tables` statistics.

To enable the extension:

```sql
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
```

## Problem

When a table is partitioned by a column (e.g., `created_at`), PostgreSQL can skip scanning partitions that don't match the query's WHERE clause (partition pruning). However, if queries don't filter on the partition key, PostgreSQL must scan ALL partitions, negating the performance benefits.

```sql
-- Table partitioned by created_at (monthly partitions)
CREATE TABLE orders (...) PARTITION BY RANGE (created_at);

-- BAD: Scans ALL partitions (no partition pruning)
SELECT * FROM orders WHERE customer_id = 123;

-- GOOD: Only scans relevant partitions
SELECT * FROM orders WHERE customer_id = 123 AND created_at > '2024-01-01';
```

## Subchecks

### partition-key-unused

Identifies partitioned tables where high-frequency queries don't use the partition key.

**Thresholds:**
- Warning: >100 calls without partition key, OR total execution time >5 minutes
- Critical: >1000 calls without partition key, OR total execution time >1 hour

### high-seq-scan-ratio

Identifies partitioned tables with excessive sequential scans compared to index scans, indicating queries may not be using partition pruning effectively.

**Thresholds:**
- Warning: seq_scan:idx_scan ratio > 10:1 AND seq_scans > 1000
- Critical: seq_scan:idx_scan ratio > 100:1 AND seq_scans > 1000

**Note:** This subcheck runs even without `pg_stat_statements` as it uses `pg_stat_user_tables` statistics aggregated from child partitions.

### join-missing-partition-key

Identifies JOINs on partitioned tables that don't include the partition key in the query. When a partitioned table is joined without filtering on its partition key, PostgreSQL must scan all partitions.

**Detection:** The check looks for queries containing JOIN clauses that reference a partitioned table but don't include the partition key column anywhere after the FROM clause (covering JOIN ON conditions, WHERE clauses, and implicit joins).

**Thresholds:**
- Warning: >100 calls with JOIN missing partition key, OR total execution time >5 minutes
- Critical: >1000 calls with JOIN missing partition key, OR total execution time >1 hour

**Example:**
```sql
-- Table partitioned by created_at
CREATE TABLE orders (...) PARTITION BY RANGE (created_at);

-- BAD: JOIN without partition key - scans ALL partitions
SELECT * FROM customers c
JOIN orders o ON o.customer_id = c.id;

-- GOOD: JOIN with partition key - enables partition pruning
SELECT * FROM customers c
JOIN orders o ON o.customer_id = c.id
WHERE o.created_at > '2024-01-01';
```

## Limitations

### Query text analysis is approximate

Uses pattern matching on query text, not full SQL parsing. May produce false positives/negatives in complex queries.

### Expression-based partition keys are skipped

Tables partitioned by expressions (not simple columns) are **excluded from this check entirely**.

```sql
-- Simple column partition key (CHECKED)
CREATE TABLE orders (...) PARTITION BY RANGE (created_at);

-- Expression-based partition key (SKIPPED)
CREATE TABLE orders (...) PARTITION BY RANGE (DATE_TRUNC('month', created_at));
CREATE TABLE events (...) PARTITION BY LIST ((status::text));
CREATE TABLE logs (...) PARTITION BY HASH ((id % 8));
```

**Why?** PostgreSQL stores expression-based keys with `attnum = 0` in `pg_partitioned_table.partattrs`, meaning there's no direct column reference. Detecting whether a query uses `DATE_TRUNC('month', created_at)` from query text is unreliable - a query filtering on `created_at` directly might still enable partition pruning depending on the expression.

These tables are silently skipped to avoid false positives.

### Subqueries and CTEs

Queries with subqueries or CTEs may not be fully analyzed.

## Verifying Partition Pruning

Use EXPLAIN to verify partition pruning:

```sql
EXPLAIN (COSTS OFF) SELECT * FROM orders WHERE created_at > '2024-01-01';

-- Good output (partitions pruned):
--   Append
--     ->  Seq Scan on orders_2024_01
--     Subplans Removed: 11

-- Bad output (all partitions scanned):
--   Append
--     ->  Seq Scan on orders_2023_01
--     ->  Seq Scan on orders_2023_02
--     ... (all partitions)
```

## How to Fix

### For `partition-key-unused`

Add partition key to queries:

```sql
-- Before
SELECT * FROM orders WHERE customer_id = $1;

-- After
SELECT * FROM orders
WHERE customer_id = $1
  AND created_at >= $2
  AND created_at < $3;
```

### For `join-missing-partition-key`

Add partition key to JOIN queries:

```sql
-- Before (scans all partitions)
SELECT * FROM customers c
JOIN orders o ON o.customer_id = c.id;

-- After (enables partition pruning)
SELECT * FROM customers c
JOIN orders o ON o.customer_id = c.id
WHERE o.created_at > '2024-01-01';
```

### For `high-seq-scan-ratio`

If sequential scans dominate, consider:

1. **Add appropriate indexes** matching query patterns
2. **Ensure partition key is used** in WHERE clauses
3. **Review query plans** with EXPLAIN ANALYZE

### General: Reconsider Partition Strategy

If most queries filter by `customer_id` but the table is partitioned by `created_at`, consider:
- Changing partition key to match query patterns
- Using composite partition key
- Hash partitioning by `customer_id` instead

### General: Accept Trade-off

For maintenance-oriented partitioning (data retention), you may accept query overhead:
- Document the decision
- Ensure indexes support the query patterns
- Monitor query performance
