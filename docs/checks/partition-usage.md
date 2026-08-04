# Partition Key Usage Check

Detects queries on partitioned tables that don't use partition keys in their WHERE clause, causing full scans across all partitions.

## Requirements

- **pg_stat_statements >= 1.9** (PostgreSQL 14+) for query pattern analysis; older versions lack the `toplevel` column and the check reports SKIP
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

Reports one row per offending statement — table, calls, total time, `queryid`, and the statement clipped to one line — so a finding can be investigated without querying `pg_stat_statements` by hand. The default detail level lists the three costliest; `--detail verbose` lists every one. Per-table totals (partition key, partition count, statements, calls, time) are always shown above the table, so they stay visible whatever the row cap.

Statement text is clipped, so read one in full with:

```sql
SELECT query FROM pg_stat_statements WHERE queryid = <Query ID>;
```

**Thresholds** apply to a table's whole unprunable workload, not to individual statements, and every row of a table carries that table's severity:
- Warning: >100 calls without partition key, OR total execution time >5 minutes
- Critical: >1000 calls without partition key, OR total execution time >1 hour

### high-seq-scan-ratio

Identifies partitioned tables with excessive sequential scans compared to index scans, indicating queries may not be using partition pruning effectively.

**Thresholds:**
- Warning: seq_scan:idx_scan ratio > 10:1 AND seq_scans > 1000
- Critical: seq_scan:idx_scan ratio > 100:1 AND seq_scans > 1000

**Note:** This subcheck runs even without `pg_stat_statements` as it uses `pg_stat_user_tables` statistics aggregated from the table's leaf partitions.

### statement-coverage

Informational: how many analyzed statements matched each partitioned table, with their call count.

A table matched by zero statements gets the same "partition keys are used" PASS as a table with a genuinely well-filtered workload — the two are indistinguishable without this. Zero usually means the application queries partition leaves directly (which prune by construction and are not attributed to the parent), or that the table's statements fall outside the analyzed sample. Least-covered tables are listed first.

This finding never changes a report's severity.

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

### Not every statement is analyzed

`pg_stat_statements` can hold tens of thousands of entries, so the check analyzes a sample: the top 500 statements by `total_exec_time` plus the top 500 by `calls` (at most 1000 after deduplication). The second axis matters — a single "costliest 500" cut systematically drops cheap high-frequency statements, which are exactly the ones where a missing partition filter compounds at scale.

A statement outside both cuts is invisible to the check, which is one reason a table can appear in `statement-coverage` with zero matched statements.

### Query text analysis is approximate

Uses pattern matching on query text, not full SQL parsing. May produce false positives/negatives in complex queries.

### Multi-level partitioning

Partition count, size and scan counters are aggregated from the whole partition tree down to the leaf tables, since an intermediate `PARTITION BY` node stores no rows itself.

Every level is reported as its own table, because each level has its own partition key: for `events` sub-partitioned into `events_2025`, both appear, and the leaves under `events_2025` count towards both rows.

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

### What counts as using the partition key

The key must appear in a comparison that can drive partition pruning, and which comparisons qualify depends on the partition strategy:

| Strategy | Prunes with | Key columns required |
|---|---|---|
| `RANGE` | `=`, `<`, `<=`, `>`, `>=`, `IN`, `BETWEEN`, `IS NULL` | the leading key column |
| `LIST` | `=`, `<`, `<=`, `>`, `>=`, `IN`, `BETWEEN`, `IS NULL` | the key column |
| `HASH` | `=`, `IN`, `IS NULL` | every key column |

`LIST` prunes on inequalities too — PostgreSQL excludes partitions whose listed values cannot satisfy the predicate. `HASH` needs every key column with equality, because the hash is only determined once all of them are known.

Mentions in `ORDER BY` or the select list don't count, and neither do `<>` or `IS NOT NULL`, which prune nothing. A comparison written with the key on the right (`$1 <= created_at`) counts — PostgreSQL commutes it.

### Only scanning statements are analyzed

Just `SELECT`, `UPDATE`, `DELETE` and `WITH` statements are considered, matched on the leading keyword. An `INSERT` routes each row to a partition by its key value and has nothing to prune, so including it would report every write as a problem. `INSERT ... SELECT` is excluded too, so a partitioned table scanned only as an insert source is not analyzed.

### Query text visibility

Only superusers and roles with `pg_read_all_stats` can read other users' query text; everyone else sees `<insufficient privilege>`. When any entry is hidden, the check reports `query-text-restricted` so a partial analysis is not mistaken for a clean bill of health.

### Partition-leaf queries

Queries referencing a partition leaf directly (e.g. `orders_2025_01`) are not attributed to the parent table — they touch exactly one partition by construction, so there is nothing to prune.

### Keys constrained through a JOIN

The key counts as used when it is constrained anywhere after `FROM`, including a `JOIN ... ON` condition. Such a query prunes when the planner parameterizes the partitioned side (a nested loop) and does not prune when it hash joins, which cannot be told from the query text. The check treats it as used, preferring silence over reporting a table whose access path may well be pruning. Confirm an individual query with:

```sql
EXPLAIN (GENERIC_PLAN, COSTS OFF) <query text with its $n placeholders>;  -- PostgreSQL 16+
```

`Subplans Removed: N` means pruning happens; all partitions listed means it does not.

### Subqueries and CTEs

Predicates inside a parenthesized subquery are ignored *unless* the subquery scans the target table — without that, any subquery filtering `s.id` would silence a table partitioned by `id`, while `FROM (SELECT * FROM orders WHERE created_at = $1) o` would be wrongly reported. CTE bodies are still analyzed, so a CTE that shadows the table name may be misattributed.

### Table aliases are not resolved

A comparison on the key column counts wherever it appears after `FROM`, without checking which relation it belongs to. So for a table partitioned by `id`, `JOIN customers c ON c.id = o.customer_id` is read as constraining the key and the query is not reported. Short, common key names are most affected. Resolving this needs real alias resolution — see [#30](https://github.com/emancu/pgdoctor/issues/30).

### Table aliases

A table referenced only through an alias (`FROM order_lines AS orders`) can be mistaken for the partitioned table of that name.

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
