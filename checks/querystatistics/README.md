# Query Statistics

Reports how long `pg_stat_statements` counters have been accumulating, and whether the
extension is actually usable.

The age is reported in the check's title, so it is visible without expanding the
finding.

## What It Checks

### Statistics Window

The period since `pg_stat_statements_reset()` was last called. Every per-statement
total — call counts, total execution time, rows — is cumulative since that point, so
the numbers other checks report are meaningless without it.

**This check never warns about a short window.** Unlike the database counters covered
by `db-statistics`, this clock is reset routinely and deliberately: engineers call
`pg_stat_statements_reset()` while investigating a slow query. A fresh window here is
normal.

### Extension Availability

`pg_stat_statements` has a half-installed state that is easy to miss:

| `pg_stat_statements.max` GUC | `pg_extension` row | Views reachable | Reported as |
|---|---|---|---|
| absent | absent | — | Not installed (PASS) |
| absent | present | — | **Not loaded (WARN)** |
| present | absent | — | Not created in this database (PASS) |
| present | present | no | **Outside search_path (WARN)** |
| present | present | yes | The window, in the title (PASS) |

`CREATE EXTENSION pg_stat_statements` succeeds even when the library is not in
`shared_preload_libraries`, but every subsequent read of the views raises
`ERROR: pg_stat_statements must be loaded via "shared_preload_libraries"`. The
extension looks installed while producing nothing, and checks that depend on it are
skipped.

The second warning covers `CREATE EXTENSION pg_stat_statements SCHEMA <schema>` where
that schema is not in the connection's `search_path`. The views exist and are
collecting data, but an unqualified read raises `42P01`, so the statistics are
unreachable from this connection. Add the schema to the `search_path` of the role
pgdoctor connects as.

## Two Independent Clocks

`pg_stat_reset()` and `pg_stat_statements_reset()` are unrelated. Resetting one leaves
the other untouched, so the window reported here does not apply to the counters
`db-statistics` reports, and vice versa.

- `db-statistics` — `pg_stat_database`, consumed by `index-usage`, `table-seq-scans`,
  `cache-efficiency`, `temp-usage`
- `query-statistics` — `pg_stat_statements`, consumed by `partition-usage`

## How to Fix

### For `query-statistics`

The only actionable state is **not loaded**:

```sql
-- Confirm the library is absent (requires a role with pg_read_all_settings)
SHOW shared_preload_libraries;
```

Add `pg_stat_statements` to `shared_preload_libraries` and restart the server. On RDS
and Aurora this is a parameter group change; `shared_preload_libraries` is static, so
it needs a reboot. On Multi-AZ that is a reboot with failover.

To inspect the window directly:

```sql
SELECT stats_reset, now() - stats_reset AS window
FROM pg_stat_statements_info;
```

## Query Details

Availability is determined from `pg_settings` and `pg_extension`, both world-readable.
`shared_preload_libraries` is deliberately not used: it is `GUC_SUPERUSER_ONLY`, and
for an unprivileged role it is silently omitted from `pg_settings` rather than
erroring — which would read as "not loaded" and produce a false warning. The
extension's own GUCs are only registered when its library initializes, so the presence
of `pg_stat_statements.max` is a reliable, unprivileged signal that it is loaded.
