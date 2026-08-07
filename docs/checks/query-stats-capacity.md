# Query Stats Capacity

Reports how full the `pg_stat_statements` entry table is and how fast it is discarding entries, because
every other check that reads that view is only as good as the sample left in it.

> **Note**: This check reads `pg_stat_statements`. When the extension is not installed, not preloaded, or
> not reachable through `search_path`, it reports PASS and stops — there is no sample to truncate.

## What It Checks

### Entry Capacity (`query-stats-capacity`)

Counts the entries currently held against `pg_stat_statements.max`. Both figures are cluster-wide, so the
count is not filtered by database.

**Severity**: PASS, always.

A full table is not a defect. A workload with more distinct statements than `max` sits pinned at `max`
indefinitely and loses nothing as long as the set is stable. This finding states the position; the
eviction rate below is what says whether entries are actually being lost.

The count comes from `pg_stat_statements(false)`, which skips the external query-text file. Reading that
text is what makes the view expensive — it materialises the whole corpus into a `work_mem` tuplestore —
and a count does not need it.

### Statement Eviction Rate (`statement-eviction-rate`)

`pg_stat_statements_info.dealloc` counts eviction *events*. Each event discards the least-recently-used
5% of `max` (`USAGE_DEALLOC_PERCENT` in `pg_stat_statements.c`), so the entries lost are
`dealloc × 0.05 × max`. Divided by the window since `pg_stat_statements_info.stats_reset`, that gives a
daily turnover expressed as a multiple of capacity.

**Severity**: WARN at or above **0.5x capacity per day**, otherwise PASS.

Half the table recycled daily is the point where the tracked set stops representing the workload.
Eviction is least-used-first, not random, so the long tail dies far sooner than the average entry
lifetime suggests: at 0.5x, anything but the hottest statements is gone within hours of running.

The grade is capped at WARN. Eviction costs nothing at runtime and degrades no query — it degrades
*observability*, and the fix requires a restart. That belongs in a sprint, not in a pager.

**Threshold on the rate, never on `dealloc` itself.** The counter only grows. A three-year-old instance
carrying `dealloc = 4000` from a bad deploy that was reverted in 2023 is perfectly healthy.

### The Measurement Window

The rate is divided by the time since `pg_stat_statements_info.stats_reset`. Unlike
`pg_stat_database.stats_reset` this is normally set — the extension stamps it at shared-memory
initialisation — but the column is nullable, and `pg_stat_statements_reset()` restarts it.

- **SKIP** when `stats_reset` is NULL: a rate over an unknown period is not a rate.
- **SKIP** when the window is under an hour: one eviction event extrapolated across a day says nothing.

SKIP rather than PASS, because a PASS here reads as "nothing is being evicted", which a window that short
cannot establish. The entry capacity finding is unaffected — it needs no window.

## Why This Matters

`pg_stat_statements` is a fixed-size hash table. Once it is full, recording a new statement means
discarding an old one, and the view gives no indication that it happened. Consequences:

- **Other checks silently analyse a fraction of the workload.** In pgdoctor that is `partition-usage`
  and `temp-usage`. A statement that is not extremely frequent is evicted before either check ever reads
  it, so both can report a confident PASS on a truncated sample.
- **Rates and totals are understated.** Cumulative counters live on the entry. Evicting it zeroes its
  history; when the statement reappears it starts from zero, and the calls, time and temp bytes it
  accumulated before are gone for good.
- **The worst offenders are the most likely to vanish.** Eviction is least-used-first. An expensive query
  that runs a few times an hour is exactly the profile that gets dropped, while a trivial one running
  thousands of times a second survives forever.
- **High turnover is itself a workload signal.** Sustained eviction means the working set of *distinct*
  normalised statements exceeds capacity — usually unparameterised SQL, variable-length `IN` lists, or
  generated DDL. Raising `max` treats the symptom.

A measured example: `dealloc = 19688` against `max = 10000` over 78 days is roughly 9.8M entries
discarded, about 126,000 per day against a capacity of 10,000. The table turned over more than twelve
times its own size every day.

## How to Fix

### Confirm the numbers

```sql
SELECT
  (SELECT count(*) FROM pg_stat_statements(false))                        AS entries,
  (SELECT setting FROM pg_settings WHERE name = 'pg_stat_statements.max') AS max_entries,
  dealloc                                                                 AS eviction_events,
  stats_reset,
  round(dealloc * 0.05 /
        (extract(epoch FROM now() - stats_reset) / 86400)::numeric, 2)    AS turnover_per_day
FROM pg_stat_statements_info;
```

### Reduce the number of distinct statements

Do this first. It is the only fix that does not require a restart, and it usually finds a real bug.
Look for statements that should have normalised to one entry and did not:

```sql
SELECT left(regexp_replace(query, '\s+', ' ', 'g'), 80) AS shape, count(*), sum(calls)
FROM pg_stat_statements
GROUP BY 1
HAVING count(*) > 10
ORDER BY count(*) DESC
LIMIT 20;
```

Common sources: string-interpolated literals instead of bind parameters, `IN ($1, $2, ..., $n)` lists of
varying length (each length is a distinct `queryid`), per-tenant table or schema names in the statement
text, and one-off DDL under `track_utility = on`.

### Raise `pg_stat_statements.max`

**This is `PGC_POSTMASTER` — it requires a full server restart, not a reload.** On RDS or Aurora that is
a reboot, and on Multi-AZ a reboot with failover. It is not a casual change; schedule it.

```sql
ALTER SYSTEM SET pg_stat_statements.max = 20000;
-- then restart the server; SELECT pg_reload_conf() will NOT apply this
```

Size it above the observed distinct-statement working set with headroom, and only after the
normalisation work above. Each entry costs shared memory allocated at startup, and the query text lives
in an external file whose size grows with the entry count.

### Reset the counter after fixing

`pg_stat_statements_reset()` zeroes `dealloc` and restarts the window, so the next run measures the new
behaviour instead of averaging it with the old. It also deletes every entry, which resets the totals
`partition-usage` and `temp-usage` read — do not do it casually on a primary.

## Important Considerations

- **Cluster-wide, not per-database.** `pg_stat_statements.max`, the entry count and `dealloc` all cover
  the whole instance. A noisy neighbour database evicts your statements.
- **`stats_reset` here is independent of `pg_stat_database.stats_reset`.** `pg_stat_statements_reset()`
  clears this window and leaves `pg_stat_database` untouched; `pg_stat_reset()` does the opposite.
- **`dealloc` needs PostgreSQL 14+** (`pg_stat_statements` 1.9), which is also the floor for
  `pg_stat_statements_info` itself.
- **Zero evictions does not mean zero blind spots.** `pg_stat_statements.track = 'none'`,
  `track_utility = off`, and statements killed by `statement_timeout` all keep work out of the view
  regardless of capacity. This check measures capacity only.
- **Not in the `triage` preset.** Nothing here changes during an incident, and the fix requires a
  restart. It is a data-quality signal, read when interpreting the checks that depend on it.

## Related Checks

- `partition-usage` - reads `pg_stat_statements` to find queries missing the partition key
- `temp-usage` - reads `pg_stat_statements` to attribute temp file writes to statements
- `extension-versions` - reports the installed `pg_stat_statements` version

## References

- [pg_stat_statements](https://www.postgresql.org/docs/current/pgstatstatements.html) - PostgreSQL documentation
- [`pg_stat_statements.c`](https://github.com/postgres/postgres/blob/master/contrib/pg_stat_statements/pg_stat_statements.c) - `USAGE_DEALLOC_PERCENT`, the 5% per eviction event
