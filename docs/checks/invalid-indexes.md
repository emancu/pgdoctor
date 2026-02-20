# Invalid Indexes Check

Identifies PostgreSQL indexes that are in an invalid state and not being used by the query planner.

## What it checks

- Indexes marked as invalid in `pg_index.indisvalid`
- Indexes that failed during concurrent creation or reindexing
- Orphaned invalid indexes taking up disk space

## Why it matters

Invalid indexes cause problems:
- **Wasted disk space**: Invalid indexes consume storage but provide no benefit
- **Query performance**: Not used by the query planner, defeating their purpose
- **Hidden failures**: May indicate underlying data quality or operational issues
- **Confusion**: Can mislead developers during query optimization

An index becomes invalid when:
- `CREATE INDEX CONCURRENTLY` fails or is interrupted
- `REINDEX CONCURRENTLY` encounters an error
- Data doesn't satisfy the index conditions

## How to Fix

For each invalid index, choose one of these options:

### Option 1: Recreate the Index

```sql
REINDEX INDEX CONCURRENTLY your_index_name;
```

**Before recreating:**
1. Investigate why the index failed initially
2. Check for locking issues or timeout problems
3. Verify the underlying data satisfies the index conditions
4. Consider if the index definition needs modification

### Option 2: Drop the Index

If the index is no longer needed:

```sql
DROP INDEX CONCURRENTLY your_index_name;
```

### Investigation Steps

1. **Check index definition:**
   ```sql
   SELECT indexdef FROM pg_indexes WHERE indexname = 'your_index_name';
   ```

2. **Review PostgreSQL logs** for errors during index creation

3. **Verify data integrity** - ensure data meets index constraints

4. **Check for lock conflicts** that might have interrupted creation

## References

- [PostgreSQL Documentation: CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)
- [PostgreSQL Documentation: REINDEX](https://www.postgresql.org/docs/current/sql-reindex.html)
