# Invalid Indexes Check

Identifies PostgreSQL indexes that are in an invalid state (`pg_index.indisvalid = false`)
and not used by the query planner — while ignoring indexes that are merely being
built right now.

## What it checks

An index ends up `indisvalid = false` for one of three reasons, and this check
treats each differently:

1. **In-flight build (silently ignored).** While a `CREATE INDEX CONCURRENTLY` or
   `REINDEX INDEX CONCURRENTLY` is running, the new index is invalid until the
   build completes. The check excludes any index that has an active
   `pg_stat_progress_create_index` row, because it is in flight, not broken.

2. **Broken index → FAIL.** A genuinely-broken index (`is_leftover = false`): a
   `CREATE INDEX CONCURRENTLY` that failed or was interrupted, leaving a permanent
   invalid index that the planner ignores. Reported under the `broken-indexes`
   subcheck as **FAIL** when any exist.

3. **Abandoned REINDEX leftover → WARN.** A `_ccnew`/`_ccold` transient
   (`is_leftover = true`) left behind by a failed or cancelled
   `REINDEX CONCURRENTLY`. The original index is still valid, so these are
   non-urgent clutter. Reported under the `abandoned-leftovers` subcheck as
   **WARN** when any exist.

Both subchecks are always emitted: each reports `OK` when its class is empty.
The overall check severity is the maximum across the two findings.

System schemas (`pg_catalog`, `information_schema`, `pg_toast`) are excluded. An
invalid index in `cron`/`pgpartman`/`debezium` is intentionally *not* excluded —
unlike a duplicate index, an invalid index there is a real operational problem.

## Why it matters

Invalid indexes cause problems:

- **Wasted disk space**: Invalid indexes consume storage but provide no benefit
- **Query performance**: Not used by the query planner, defeating their purpose
- **Hidden failures**: May indicate underlying data quality or operational issues
- **Confusion**: Can mislead developers during query optimization

## How to Fix

### Broken indexes (FAIL)

For each broken index, choose one of:

**Rebuild the index** (preferred when the index is still wanted):

```sql
REINDEX INDEX CONCURRENTLY your_schema.your_index_name;
```

**Drop the index** (if it is no longer needed):

```sql
DROP INDEX CONCURRENTLY your_schema.your_index_name;
```

Before rebuilding:

1. Investigate why the index failed initially
2. Check for locking issues or timeout problems
3. Verify the underlying data satisfies the index conditions
4. Consider if the index definition needs modification

### Abandoned REINDEX leftovers (WARN)

The original index is still valid; the `_ccnew`/`_ccold` leftover can simply be
dropped:

```sql
DROP INDEX CONCURRENTLY your_schema.your_leftover_index;
```

### Investigation Steps

1. **Check index definition:**
   ```sql
   SELECT indexdef FROM pg_indexes WHERE indexname = 'your_index_name';
   ```
2. **Review PostgreSQL logs** for errors during index creation
3. **Verify data integrity** — ensure data meets index constraints
4. **Check for lock conflicts** that might have interrupted creation

## Notes / caveats

- **Read replicas.** On a standby, an in-flight build on the primary can briefly
  surface its `_ccnew` index as a WARN: the standby sees the invalid index but
  `pg_stat_progress_create_index` reflects only local backends, so the in-flight
  exclusion does not apply. It clears once the primary finishes and the change
  replays.
- **Naming collisions.** A user index literally named `*_ccnew`/`*_ccold` that is
  genuinely invalid would be classified as a WARN leftover rather than a FAIL.
  This is rare and only affects severity, not detection.
- **Classification can change between runs.** Whether a given `_ccnew` index is
  reported (and as what) can change from one run to the next as a concurrent
  reindex makes progress, completes, or its backend dies and leaves the transient
  behind.

## References

- [PostgreSQL Documentation: CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)
- [PostgreSQL Documentation: REINDEX](https://www.postgresql.org/docs/current/sql-reindex.html)
- [PostgreSQL Documentation: pg_stat_progress_create_index](https://www.postgresql.org/docs/current/progress-reporting.html#CREATE-INDEX-PROGRESS-REPORTING)
