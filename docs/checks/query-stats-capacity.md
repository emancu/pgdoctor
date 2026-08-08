# Query Stats Capacity

Reports how full the `pg_stat_statements` entry table is and how fast it is discarding entries, because
every other check that reads that view is only as good as the sample left in it.

> **Note**: This check reads `pg_stat_statements`. When the extension is not installed, not preloaded, or
> not reachable through `search_path`, it reports SKIP: the table is cluster-wide, so its absence here says nothing
> about whether the shared hash is evicting, and nothing was inspected.

## What It Checks

### Entry Usage (`entry-usage`)

Prints the entries currently held against `pg_stat_statements.max`, as `9.9K/10.0K entries`. Both figures
are cluster-wide, so neither is filtered by database.

**Severity**: PASS, always. SKIP when `pg_stat_statements` cannot be read at all.

Occupancy is reported, not graded. A full table is the normal steady state for any workload with more
distinct statements than `max`, and on its own it says nothing about whether entries are still being
lost — that is what `statement-eviction-rate` measures.

The count comes from `pg_stat_statements(false)`, which skips the external query-text file. Reading that
text is what makes the view expensive: it materialises the whole corpus into a `work_mem` tuplestore, and
a count does not need it.

### Statement Eviction Rate (`statement-eviction-rate`)

Prints how long the table takes to turn over once, and the window that average is drawn from, as
`table recycled every 1.9h (78d average)`.

`pg_stat_statements_info.dealloc` counts eviction *events*, not entries. `entry_dealloc()` in
`pg_stat_statements.c` discards a fixed batch per event:

```c
nvictims = Max(10, pgss_max * USAGE_DEALLOC_PERCENT / 100);   /* USAGE_DEALLOC_PERCENT is 5 */
```

So the entries lost are `dealloc × Max(10, max × 5 / 100)`, and capacity divided by that rate is the
recycle time. **The floor is not a rounding detail.** `pg_stat_statements.max` bottoms out at 100, and
below `max = 200` the floor of 10 exceeds 5%. At `max = 100` each event discards a tenth of capacity
rather than a twentieth; assuming a flat 5% there would report half the real turnover and pass a
saturated instance.

**Severity**: WARN when the table recycles in **48 hours or less**, otherwise PASS. SKIP when the window
is too short to distinguish a real rate from a single eviction event.

Recycling the whole table in under two days is where the tracked set stops representing the workload.
Eviction is not age-based: entries are ranked by a usage counter that gains 1.0 per execution and decays
by 1% each pass, so the *least frequently executed* statements are dropped first regardless of how
recently they ran. The tail therefore dies far sooner than the average recycle time suggests.

The grade is capped at WARN. Eviction costs nothing at runtime and degrades no query. It degrades
*observability*, and the fix requires a restart. That belongs in a sprint, not in a pager.

**Threshold on the rate, never on `dealloc` itself.** The counter only grows. A three-year-old instance
carrying `dealloc = 4000` from a bad deploy that was reverted in 2023 is perfectly healthy.

## Why This Matters

`pg_stat_statements` is a fixed-size hash table. Once it is full, recording a new statement means
discarding an old one, and the view gives no indication that it happened. Consequences:

- **Other checks silently analyse a fraction of the workload.** In pgdoctor that is `partition-usage`
  and `temp-usage`. A statement that is not run frequently is evicted before either check ever reads it,
  so both can report a confident PASS on a truncated sample.
- **Rates and totals are understated.** Cumulative counters live on the entry. Evicting it zeroes its
  history; when the statement reappears it starts from zero, and the calls, time and temp bytes it
  accumulated before are gone for good. A surviving entry can therefore still be undercounted.
- **The worst offenders are the most likely to vanish.** Ranking is by execution count, so an expensive
  query that runs a few times an hour is exactly the profile that gets dropped, while a trivial one
  running thousands of times a second survives forever.
- **High turnover is itself a workload signal.** Sustained eviction means the working set of *distinct*
  normalised statements exceeds capacity: usually unparameterised SQL, variable-length `IN` lists, or
  generated DDL. Raising `max` treats the symptom.

A measured example: `dealloc = 19688` against `max = 10000` over 78 days is roughly 9.8M entries
discarded, about 126,000 per day against a capacity of 10,000 — the table turning over more than twelve
times its own size daily, or once every 1.9 hours.

## How to Fix

### For `entry-usage`

PASS is informational and never needs action. A SKIP does: it means `pg_stat_statements` could not be
read, so `partition-usage` and `temp-usage` are running blind too. Install the extension and confirm the
library is preloaded — being installed is not sufficient, because `CREATE EXTENSION` succeeds without
`shared_preload_libraries` and every subsequent read then errors.

```sql
SHOW shared_preload_libraries;          -- must list pg_stat_statements
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
```

### For `statement-eviction-rate`

#### Confirm the numbers

`pgdoctor explain query-stats-capacity` prints the query, which derives every figure in the finding.

#### Reduce the number of distinct statements

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

#### Raise `pg_stat_statements.max`

**This is `PGC_POSTMASTER`: it requires a full server restart, not a reload.** On RDS or Aurora that is
a reboot, and on Multi-AZ a reboot with failover. It is not a casual change; schedule it.

```sql
ALTER SYSTEM SET pg_stat_statements.max = 20000;
-- then restart the server; SELECT pg_reload_conf() will NOT apply this
```

Size it above the observed distinct-statement working set with headroom, and only after the
normalisation work above. Each entry costs shared memory allocated at startup, and the query text lives
in an external file whose size grows with the entry count.

#### Reset the counter after fixing

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
- **The rate is an average over the whole window.** A burst of eviction that stopped a month ago still
  reports as steady turnover, because `dealloc` is cumulative and a single snapshot cannot date it.
  Comparing the count between two runs over a known interval establishes recency.
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
