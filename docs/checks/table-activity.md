# Table Activity Check

Describes table write patterns: which tables churn hardest and how efficiently their updates avoid index work.

> **Note**: This check reads PostgreSQL runtime statistics. For meaningful numbers, statistics should be at least
> 7 days old. Run the `statistics-freshness` check to validate statistics maturity.

Both findings are **informational** — they characterize the workload, they do not flag a defect.

## What It Checks

### High Churn Tables (`high-churn-tables`)

Lists tables whose cumulative write count (`n_tup_ins + n_tup_upd + n_tup_del`) exceeds 1 million total writes.

- **INFO**: more than 1 million total writes

### Low HOT Ratio (`low-hot-ratio`)

Lists tables over 1 million live rows whose HOT ratio (`n_tup_hot_upd / n_tup_upd`) is below 50%, meaning most
updates take the non-HOT path.

- **INFO**: more than 1 million live rows and HOT ratio below 50%

## Why This Matters

### High churn

Churn is cumulative write activity since statistics were last reset — a lifetime count, not a rate. High churn is
a property of the workload, not a fault. Updates and deletes leave dead tuples behind for vacuum, so on
update/delete-heavy tables churn sets the pace of:

- **Vacuum pressure** — more dead tuples, so autovacuum must run more often to keep up.
- **Bloat velocity** — when vacuum falls behind, dead tuples accumulate into table and index bloat.
- **WAL volume** — every write (inserts included) is logged, driving replication traffic and backup size.

Insert-only tables can exceed the threshold too; they create no dead tuples, so for them churn speaks to WAL,
statistics freshness, and growth — not vacuum debt.

Read it as context for the vacuum checks: a table flagged by `table-vacuum-health` or `table-bloat` is easier to
interpret once you know whether it is high-churn.

### Low HOT ratio

A **HOT (Heap-Only Tuple)** update writes the new row version on the same page and skips updating every index — the
update stays inside the heap. HOT applies only when no indexed column changed and the page has free space for the
new version.

A low ratio costs because a non-HOT update must insert a new entry into **every** index on the table:

- **Write amplification** — one row update becomes many index writes, plus extra WAL.
- **Index bloat** — the old index entries are left dead, and only `REINDEX` reclaims that space.

## How to Fix

### For `low-hot-ratio`

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
