# Table Activity Check

Describes table write patterns: which tables churn hardest and how efficiently their updates avoid index work.

> **Note**: This check reads PostgreSQL runtime statistics. For meaningful numbers, statistics should be at least
> 7 days old. Run the `statistics-freshness` check to validate statistics maturity.

Both findings are **informational** — they characterize the workload, they do not flag a defect.

## High Churn Tables (`high-churn-tables`)

Churn is cumulative write activity: `n_tup_ins + n_tup_upd + n_tup_del` since statistics were last reset — a
lifetime count, not a rate. This finding lists tables above 1 million total writes.

High churn is a property of the workload, not a fault. Updates and deletes leave dead tuples behind for vacuum,
so on update/delete-heavy tables churn sets the pace of:

- **Vacuum pressure** — more dead tuples, so autovacuum must run more often to keep up.
- **Bloat velocity** — when vacuum falls behind, dead tuples accumulate into table and index bloat.
- **WAL volume** — every write (inserts included) is logged, driving replication traffic and backup size.

Insert-only tables can exceed the threshold too; they create no dead tuples, so for them churn speaks to WAL,
statistics freshness, and growth — not vacuum debt.

Read it as context for the vacuum checks: a table flagged by `table-vacuum-health` or `table-bloat` is easier to
interpret once you know whether it is high-churn.

## Low HOT Ratio (`low-hot-ratio`)

A **HOT (Heap-Only Tuple)** update writes the new row version on the same page and skips updating every index — the
update stays inside the heap. HOT applies only when no indexed column changed and the page has free space for the
new version.

The ratio is `n_tup_hot_upd / n_tup_upd`. This finding lists tables over 1 million live rows whose ratio is below
50%, meaning most updates take the non-HOT path.

A low ratio costs because a non-HOT update must insert a new entry into **every** index on the table:

- **Write amplification** — one row update becomes many index writes, plus extra WAL.
- **Index bloat** — the old index entries are left dead, and only `REINDEX` reclaims that space.

Two levers raise the ratio:

- **Leave page headroom** — lower `fillfactor` so pages keep room for in-place new versions:
  ```sql
  ALTER TABLE your_table SET (fillfactor = 90);
  ```
  This affects new pages only; existing data needs a rewrite (`VACUUM FULL` or `pg_repack`) to benefit.
- **Avoid updating indexed columns** — one indexed column that changes on every update (an indexed `updated_at`, an
  indexed `status` you mutate) disables HOT. Drop the index or make it partial if the workload allows.

## Related Checks

- `table-bloat` — dead tuple accumulation, the downstream effect of unchecked churn.
- `table-vacuum-health` — whether vacuum keeps pace with the churn above.
- `freeze-age` — transaction ID wraparound risk.
