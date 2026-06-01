# Invalid Indexes

Identifies indexes left in an invalid state (`pg_index.indisvalid = false`). The planner ignores invalid indexes, so they cost disk and maintenance without serving any query.

## What It Checks

Every invalid index is reported as **WARN** and tagged with a `Type` so you know how to act on it. Indexes a concurrent build is *currently* working on are excluded — they are invalid only until the build finishes.

### Broken (`broken`)
A permanently-invalid index: a `CREATE INDEX CONCURRENTLY` that failed or was interrupted, or any other index PostgreSQL has marked invalid.

**Severity**: WARN

**Example**:
```sql
CREATE INDEX CONCURRENTLY idx_users_email ON users(email);
-- interrupted or failed -> idx_users_email stays indisvalid = false
```

### Abandoned REINDEX leftover (`leftover`)
A `_ccnew`/`_ccold` transient left behind by a failed or cancelled `REINDEX INDEX CONCURRENTLY`. The original index is still valid and in use, so the leftover is harmless clutter.

**Severity**: WARN

**Example**:
```sql
REINDEX INDEX CONCURRENTLY idx_orders_status;
-- cancelled mid-build -> idx_orders_status_ccnew left behind, invalid
```

### Excluded: builds in flight
While a `CREATE INDEX CONCURRENTLY` or `REINDEX INDEX CONCURRENTLY` is running, the new index is invalid until the build completes. These have an active `pg_stat_progress_create_index` row and are excluded — in flight, not broken.

System schemas (`pg_catalog`, `information_schema`, `pg_toast`) are excluded. Unlike duplicate indexes, an invalid index in `cron`/`pgpartman`/`debezium` *is* reported — there, it is a real operational problem.

## Why It Matters

Invalid indexes cause problems:

- **Wasted disk space**: they consume storage but provide no benefit
- **Query performance**: the planner ignores them, defeating their purpose
- **Hidden failures**: often a sign of an interrupted migration or operational issue
- **Confusion**: can mislead developers during query optimization

## How to Fix

### Broken indexes
Rebuild it (preferred if the index is still wanted) or drop it:

```sql
REINDEX INDEX CONCURRENTLY your_schema.your_index;
-- or, if it is no longer needed:
DROP INDEX CONCURRENTLY your_schema.your_index;
```

Check the PostgreSQL logs for the original failure before rebuilding.

### Abandoned leftovers
The original index is still valid — just drop the leftover:

```sql
DROP INDEX CONCURRENTLY your_schema.your_leftover;
```

## Important Considerations

- **Read replicas**: on a standby, an in-flight build on the primary can briefly show its `_ccnew` as a `leftover` — `pg_stat_progress_create_index` reflects local backends only. It clears once the build replays.
- **Naming collisions**: a user index literally named `*_ccnew`/`*_ccold` that is genuinely invalid would be tagged `leftover` rather than `broken`. Rare, and only affects the label.

## References

- [PostgreSQL: CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)
- [PostgreSQL: REINDEX](https://www.postgresql.org/docs/current/sql-reindex.html)
- [PostgreSQL: pg_stat_progress_create_index](https://www.postgresql.org/docs/current/progress-reporting.html#CREATE-INDEX-PROGRESS-REPORTING)
