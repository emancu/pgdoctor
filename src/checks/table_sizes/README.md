# Table Sizes Check

**ID**: `table_sizes`
**Category**: `storage`

## Description

This check analyzes all user tables in the database and identifies tables that may be growing too large. Large tables can impact query performance, backup times, and maintenance operations. This check helps you proactively identify tables that may benefit from partitioning, archiving, or optimization strategies.

## Query

```sql
SELECT
    schemaname,
    tablename,
    pg_total_relation_size(schemaname||'.'||tablename) as size_bytes
FROM pg_tables
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
ORDER BY size_bytes DESC
```

This query:
- Returns all user tables (excludes system catalogs)
- Calculates the total size including indexes, TOAST tables, and free space
- Orders results by size (largest first)

## Validations

### 1. Total Database Size

Reports the total size of all user tables in the database.

- **Status**: Always OK (informational)
- **Message**: Shows total size and table count

### 2. Large Tables Detection

Identifies individual tables that exceed size thresholds:

- **WARN**: Tables > 10 GB
  - These tables are large and should be monitored
  - Consider optimization strategies if growth continues

- **CRITICAL**: Tables > 50 GB
  - These tables are very large and may impact performance
  - Strongly consider partitioning, archiving, or data lifecycle management

- **OK**: No tables exceed thresholds
  - All tables are within reasonable size limits

## Example Output

### Healthy Database
```
✓ [OK] Total database size: 2.45 GB across 12 table(s)
✓ [OK] No unusually large tables detected.
```

### Warning State
```
✓ [OK] Total database size: 18.73 GB across 8 table(s)
⚠ [WARN] Table public.events is large: 15.20 GB. Monitor growth and consider optimization.
```

### Critical State
```
✓ [OK] Total database size: 127.45 GB across 15 table(s)
✗ [CRITICAL] Table public.logs is very large: 65.80 GB. Consider partitioning or archiving.
⚠ [WARN] Table public.analytics is large: 12.30 GB. Monitor growth and consider optimization.
```

## Troubleshooting

### How to check table sizes manually

```sql
SELECT
    schemaname,
    tablename,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as total_size,
    pg_size_pretty(pg_relation_size(schemaname||'.'||tablename)) as table_size,
    pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename) - pg_relation_size(schemaname||'.'||tablename)) as external_size
FROM pg_tables
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
```

### Strategies for Large Tables

1. **Table Partitioning**
   - Split large tables into smaller, manageable partitions
   - Improves query performance and maintenance operations
   - Best for time-series or range-based data

2. **Archiving**
   - Move old data to archive tables or separate databases
   - Keep active tables smaller and more performant
   - Implement data retention policies

3. **Indexing**
   - Review and optimize indexes
   - Remove unused indexes (they consume space)
   - Ensure indexes are being used effectively

4. **VACUUM and ANALYZE**
   - Regular maintenance prevents bloat
   - Keeps table statistics up to date
   - Consider autovacuum tuning for large tables

5. **Compression**
   - Use TOAST compression for large text/binary fields
   - Consider table compression options (if available in your PG version)

### Investigating Table Growth

```sql
-- Check table bloat
SELECT
    schemaname,
    tablename,
    pg_size_pretty(pg_relation_size(schemaname||'.'||tablename)) as size,
    n_dead_tup,
    n_live_tup,
    round(n_dead_tup * 100.0 / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_tuple_percent
FROM pg_stat_user_tables
ORDER BY n_dead_tup DESC;
```

## References

- [PostgreSQL Table Partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html)
- [Managing Disk Space](https://www.postgresql.org/docs/current/disk-usage.html)
- [VACUUM Documentation](https://www.postgresql.org/docs/current/sql-vacuum.html)
- [Database Object Size Functions](https://www.postgresql.org/docs/current/functions-admin.html#FUNCTIONS-ADMIN-DBSIZE)
