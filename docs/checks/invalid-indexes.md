# Invalid Indexes Check

Finds indexes stuck in an invalid state (`pg_index.indisvalid = false`). The
planner ignores them, so they cost disk without helping queries.

## What it checks

Reports each invalid index as **WARN**, classified by `Type`:

- **broken** — a `CREATE INDEX CONCURRENTLY` that failed or was interrupted, or
  any other permanently-invalid index.
- **leftover** — a `_ccnew`/`_ccold` transient left behind by a failed or
  cancelled `REINDEX CONCURRENTLY`. The original index is still valid.

Indexes a `CREATE`/`REINDEX INDEX CONCURRENTLY` is *currently* building are
excluded — they are invalid only until the build finishes (in flight, not
broken). System schemas (`pg_catalog`, `information_schema`, `pg_toast`) are
excluded; `cron`/`pgpartman`/`debezium` are not, since an invalid index there is
a real problem.

## How to fix

```sql
-- broken: rebuild, or drop if the index is no longer wanted
REINDEX INDEX CONCURRENTLY your_schema.your_index;
DROP INDEX CONCURRENTLY your_schema.your_index;

-- leftover: just drop it, the original index is still valid
DROP INDEX CONCURRENTLY your_schema.your_leftover;
```

For a broken index, check the PostgreSQL logs for the original failure before
rebuilding.

## Notes

- On a read replica, an in-flight build on the primary can briefly show its
  `_ccnew` as a leftover (`pg_stat_progress_create_index` is local-only); it
  clears once the build replays.
- A user index literally named `*_ccnew`/`*_ccold` would be labelled `leftover`.

## References

- [CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)
- [REINDEX](https://www.postgresql.org/docs/current/sql-reindex.html)
