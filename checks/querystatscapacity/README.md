# Query Stats Capacity

Reports how full the `pg_stat_statements` entry table is and how fast it is discarding entries, because
every other check that reads that view is only as good as the sample left in it.

> **Note**: This check reads `pg_stat_statements`. When the extension is not installed, not preloaded, or
> not reachable through `search_path`, it reports SKIP: the table is cluster-wide, so its absence here says nothing
> about whether the shared hash is evicting, and nothing was inspected.

## What It Checks

### Entry Usage (`entry-usage`)

Counts the entries currently held against `pg_stat_statements.max`. Both figures are cluster-wide, so the
count is not filtered by database.

**Severity**: WARN when the table is full **and** entries have been evicted, otherwise PASS. SKIP when
`max` is unreadable, since there is nothing for "full" to be relative to.

Occupancy on its own is not a defect. A stable workload with more distinct statements than `max` sits
pinned there indefinitely and loses nothing, and below capacity there is headroom. The two together are
what matters, because that is the state the rate below understates: it averages evictions over the whole
window, so churn that began recently barely moves it while the table is actively losing entries.

The count comes from `pg_stat_statements(false)`, which skips the external query-text file. Reading that
text is what makes the view expensive: it materialises the whole corpus into a `work_mem` tuplestore, and
and a count does not need it.

### Statement Eviction Rate (`statement-eviction-rate`)

`pg_stat_statements_info.dealloc` counts eviction *events*, not entries. `entry_dealloc()` in
`pg_stat_statements.c` discards a fixed batch of least-recently-used entries per event:

```c
nvictims = Max(10, pgss_max * USAGE_DEALLOC_PERCENT / 100);   /* USAGE_DEALLOC_PERCENT is 5 */
```

So the entries lost are `dealloc × Max(10, max × 5 / 100)`. **The floor is not a rounding detail.**
`pg_stat_statements.max` bottoms out at 100, and below `max = 200` the floor of 10 exceeds 5%. At
`max = 100` each event discards 10 entries, a tenth of capacity rather than a twentieth. Assuming a flat
5% there would report half the real turnover and pass a saturated instance.

Dividing the entries lost by the window since `pg_stat_statements_info.stats_reset`, and then by `max`,
gives a daily turnover expressed as a multiple of capacity.

**Severity**: WARN at or above **0.5x capacity per day**, otherwise PASS.

The displayed figure is truncated to one decimal, not rounded, so it can only ever understate the
measurement: 0.498x prints as `0.4x capacity/day` rather than showing `0.5x` on a finding that passed at
a 0.5 threshold. A nonzero rate below the display floor prints as `<0.1x capacity/day` instead of
collapsing to `0.0x`, which would read as no eviction at all.

Half the table recycled daily is the point where the tracked set stops representing the workload.
Eviction is least-used-first, not random, so the long tail dies far sooner than the average entry
lifetime suggests: at 0.5x, anything but the hottest statements is gone within hours of running.

The grade is capped at WARN. Eviction costs nothing at runtime and degrades no query. It degrades
*observability*, and the fix requires a restart. That belongs in a sprint, not in a pager.

**Threshold on the rate, never on `dealloc` itself.** The counter only grows. A three-year-old instance
carrying `dealloc = 4000` from a bad deploy that was reverted in 2023 is perfectly healthy.

### The Measurement Window

The rate is divided by the time since `pg_stat_statements_info.stats_reset`. Unlike
`pg_stat_database.stats_reset` this is normally set, since the extension stamps it at shared-memory
initialisation, but the column is nullable and `pg_stat_statements_reset()` restarts it.

- **SKIP** when `stats_reset` is NULL: a rate over an unknown period is not a rate.
- **SKIP** when the window is under an hour: one eviction event extrapolated across a day says nothing.
- **SKIP** when `pg_stat_statements.max` is unreadable: both the batch size and the capacity the turnover
  is a share of derive from it.

SKIP rather than PASS, because a PASS here reads as "nothing is being evicted", which a window that short
cannot establish. The entry usage finding is unaffected, since it needs neither the window nor `max`.

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
  normalised statements exceeds capacity: usually unparameterised SQL, variable-length `IN` lists, or
  generated DDL. Raising `max` treats the symptom.

A measured example: `dealloc = 19688` against `max = 10000` over 78 days is roughly 9.8M entries
discarded, about 126,000 per day against a capacity of 10,000. The table turned over more than twelve
times its own size every day.

## How to Fix

### Confirm the numbers

```sql
WITH cap AS (
  SELECT (SELECT setting FROM pg_settings
          WHERE name = 'pg_stat_statements.max')::bigint AS max_entries
)
SELECT
  (SELECT count(*) FROM pg_stat_statements(false))                    AS entries,
  cap.max_entries,
  i.dealloc                                                           AS eviction_events,
  greatest(10, cap.max_entries * 5 / 100)                             AS entries_per_event,
  i.stats_reset,
  round((i.dealloc * greatest(10, cap.max_entries * 5 / 100))::numeric
        / cap.max_entries
        / (extract(epoch FROM now() - i.stats_reset) / 86400)::numeric, 2) AS turnover_per_day
FROM pg_stat_statements_info AS i
CROSS JOIN cap;
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

**This is `PGC_POSTMASTER`: it requires a full server restart, not a reload.** On RDS or Aurora that is
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
`partition-usage` and `temp-usage` read, so do not do it casually on a primary.

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
