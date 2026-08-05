# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]


### Added

- **`extension-versions`**: new check inventorying installed extensions — `version-support` flags versions unsupported upstream (WARN deprecated, FAIL unsupported) against embedded floors for `pg_partman` and `postgis`, and `pending-update` warns when an installed version trails the version bundled on disk ([#46](https://github.com/emancu/pgdoctor/pull/46)).
- **`cache-efficiency`**: new informational `table-cache-ratio` finding listing hot tables (top-20 by reads or ≥1% of read traffic, and ≥10,000 reads) over 500MB with a heap cache-hit ratio below 75% ([#37](https://github.com/emancu/pgdoctor/pull/37)).
- **check**: new `SeverityInfo` level for informational findings that never escalate a report's severity ([#19](https://github.com/emancu/pgdoctor/pull/19)).
- **`check.Table`**: new optional `MaxRowsBrief` field overriding the renderer's 10-row cap at the default detail level; column widths are now sized from the rows actually shown, so a long value in a hidden row no longer stretches the table ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: new `query-text-restricted` finding — warns when `pg_stat_statements` hides query text from the current role, instead of reporting PASS on a partial workload ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`toast-storage`**: new `compression-default` finding — warns when the cluster `default_toast_compression` is not lz4 ([#41](https://github.com/emancu/pgdoctor/pull/41)).

### Fixed

- **`partition-usage`**: `partition-key-unused` now states the period its call and time totals cover — `pg_stat_statements` counters are cumulative since the last reset, so the numbers were uninterpretable on their own ([#47](https://github.com/emancu/pgdoctor/pull/47)).

- **`table-bloat`**: `stale-vacuum` FAIL is now a strict subset of WARN, reports escalate correctly, and `high-dead-tuples` no longer reports FAIL ([#20](https://github.com/emancu/pgdoctor/pull/20)).
- **`partition-usage`**: analyzes complete `pg_stat_statements` query text (current database, top-level statements only), matches tables and partition keys on SQL identifier boundaries, and requires a pruning-capable comparison — partition-leaf queries, lookalike column names, and `ORDER BY`-only key mentions no longer skew results ([#25](https://github.com/emancu/pgdoctor/pull/25)).
- **`partition-usage`**: sub-partitioned tables are measured from their leaf partitions. `pg_total_relation_size()` returns 0 for an intermediate `PARTITION BY` node and such nodes carry no `pg_stat_user_tables` counters, so a multi-level parent reported 0 bytes and 0 scans — `high-seq-scan-ratio` could never fire for it and results were ordered by a size that was mostly missing. `Partitions` now counts leaves, not direct children ([#47](https://github.com/emancu/pgdoctor/pull/47)).
- **`partition-usage`**: a plain `UPDATE` is analyzed again — it has no `FROM` clause, so scanning only after `FROM` reported every update regardless of its `WHERE` ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: `LIST` partitions now count inequalities as pruning, matching PostgreSQL, which excludes partitions whose listed values cannot satisfy the predicate. Only `HASH` requires equality on every key column ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: a subquery that scans the target table keeps its predicate, so `FROM (SELECT … FROM orders WHERE created_at = $1) o` is no longer reported; subqueries on other tables are still ignored ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: schema scoping handles independently quoted identifiers, so `tenant_a."orders"` is attributed to `tenant_a` and not to a sibling schema ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: a CTE wrapping an `INSERT` no longer slips past the statement-type filter ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: no longer reports `INSERT` statements as missing the partition key. Statement type is matched on the leading keyword, where before `ILIKE '%UPDATE%'` accepted every insert carrying an `updated_at` column — which on Rails and Ecto schemas meant the highest-traffic writes on every partitioned table ([#32](https://github.com/emancu/pgdoctor/pull/32)).

### Removed

- **`connection-efficiency`**: retired the `busy-ratio` finding — a point-in-time active/total ratio is meaningless under transaction pooling; `connection-health/idle-ratio` covers the signal ([#47](https://github.com/emancu/pgdoctor/pull/47)).
- **`table-bloat`**: retired the `stale-vacuum` finding — vacuum freshness is covered by `table-vacuum-health/vacuum-stale` ([#26](https://github.com/emancu/pgdoctor/pull/26)).
- **`table-vacuum-health`**: retired the `analyze-needed` finding — absorbed by `vacuum-stale` ([#27](https://github.com/emancu/pgdoctor/pull/27)).
- **`toast-storage`**: retired the `large-toast` finding — absorbed by the merged `toast-ratio`; its ID and 10GB/100GB WARN/FAIL tiers are gone ([#41](https://github.com/emancu/pgdoctor/pull/41)).
- **`toast-storage`**: retired the `wide-columns` finding — it measured stored width (a TOAST pointer for large values) and could not detect wide columns ([#45](https://github.com/emancu/pgdoctor/pull/45)).

### Changed

- **`connection-efficiency`**: `sessions-abandoned` warns above 7% and no longer reports FAIL ([#47](https://github.com/emancu/pgdoctor/pull/47)).
- **`toast-storage`**: merged `toast-ratio` and `large-toast` into one informational `toast-ratio` finding listing TOAST-heavy tables (>=50% ratio or >=10GB), sorted by TOAST size desc; it never escalates the report ([#41](https://github.com/emancu/pgdoctor/pull/41)).

- **`toast-storage`**: `compression-algorithm` now counts effective pglz (explicit, or unset while `default_toast_compression` is pglz) and moves its big-TOAST itemization to `--detail debug` ([#41](https://github.com/emancu/pgdoctor/pull/41)).
- **check**: renamed `SeverityOK` to `SeverityPass` — breaking for library consumers ([#19](https://github.com/emancu/pgdoctor/pull/19)).
- **`cache-efficiency`**: `cache-hit-ratio` is now informational, no longer escalates the report, and reports only below a 60% cache-hit ratio ([#37](https://github.com/emancu/pgdoctor/pull/37)).
- **`cache-efficiency`**: `index-cache-ratio` moved from `index-usage`; the old `index-usage/index-cache-ratio` finding ID is retired — consumers must switch to `cache-efficiency/index-cache-ratio` ([#37](https://github.com/emancu/pgdoctor/pull/37)).
- **`cache-efficiency`**: `index-cache-ratio` now lists only hot indexes (top-20 by scans or ≥1% of scan traffic, and ≥10,000 scans) over 500MB with a cache-hit ratio below 75% ([#37](https://github.com/emancu/pgdoctor/pull/37)).
- **`pk-types`**: reports int4/int2 primary keys only from 45% capacity usage, FAIL from 85% ([#31](https://github.com/emancu/pgdoctor/pull/31)).
- **`index-usage`**: `index-cache-ratio` is now informational and no longer escalates the report ([#34](https://github.com/emancu/pgdoctor/pull/34)).
- **`index-usage`**: `unused-indexes` reports only indexes over 500MB and discloses the statistics window ([#34](https://github.com/emancu/pgdoctor/pull/34)).
- **`index-usage`**: `low-usage-indexes` now flags sustained low read rates instead of lifetime scan counts ([#34](https://github.com/emancu/pgdoctor/pull/34)).
- **`index-usage`**: `low-usage-indexes` is now informational — acting on it safely requires cluster-wide verification ([#38](https://github.com/emancu/pgdoctor/pull/38)).
- **`partition-usage`**: statements are now sampled on two axes — the top 500 by `total_exec_time` plus the top 500 by `calls` — instead of the costliest 500 only. The old cut systematically dropped cheap high-frequency statements, precisely where a missing partition filter compounds at scale, so tables whose unprunable workload is high-volume rather than slow can now be reported ([#47](https://github.com/emancu/pgdoctor/pull/47)).
- **`partition-usage`**: `partition-key-unused` now reports one row per offending statement — table, calls, total time, `queryid` and clipped text — instead of one aggregate row per table, so a finding can be investigated without querying `pg_stat_statements` by hand. Per-table totals moved above the table and are always shown; the statement list is capped at three by default and complete under `--detail verbose` ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: `partition-key-unused` now counts a partition key constrained anywhere after `FROM`, including `JOIN ... ON` conditions, instead of only in `WHERE`. Such queries prune whenever the planner parameterizes the partitioned side, and reporting them buried the genuinely unprunable ones. The finding is also renamed to "Queries Missing Partition Key", matching its `join-missing-partition-key` sibling ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`partition-usage`**: partition pruning is now judged per strategy — HASH and LIST need equality on the key (HASH on every key column), RANGE needs its leading column — and queries qualified by another schema are no longer attributed to a same-named table ([#32](https://github.com/emancu/pgdoctor/pull/32)).
- **`table-vacuum-health`**: `vacuum-stale` now lists only tables with real pending work and covers analyze staleness too ([#27](https://github.com/emancu/pgdoctor/pull/27)).
- **`table-vacuum-health`**: `large-table-defaults` now shows when default settings would next trigger autovacuum, and no longer reports FAIL ([#28](https://github.com/emancu/pgdoctor/pull/28)).
- **`vacuum-settings`**: RAM-budget findings are now a single line; the full breakdown moved to `--detail debug` ([#21](https://github.com/emancu/pgdoctor/pull/21)).
- **`index-bloat`**: `high-bloat` and `large-bloat` merged into the check's single finding; both old finding IDs are retired ([#23](https://github.com/emancu/pgdoctor/pull/23)).
- **`cache-efficiency`**: no longer reports FAIL; warns only below a 90% cache-hit ratio ([#16](https://github.com/emancu/pgdoctor/pull/16)).

## [0.3.0] - 2026-06-01

### Added

- **`invalid-indexes`**: classifies abandoned `_ccnew`/`_ccold` leftovers from a cancelled `REINDEX CONCURRENTLY` as a droppable `leftover`, distinct from genuinely-broken indexes (shown via a `Type` column).
- **`replication-lag`**: capacity-relative signal for logical slots — compares the backlog against `max_slot_wal_keep_size` (≥50% warn, ≥85% fail), firing before Postgres flips `wal_status` to `unreserved`. Disabled when the cap is unlimited (`-1`, the RDS default).
- **`check.ParseDurationMs`**: exported helper that parses GUC duration values (`2000ms`, `2s`, `1min`, `1.5s`, bare numbers, `-1`/`0` sentinels) to milliseconds.

### Changed

- **`invalid-indexes`**: excludes indexes a live `CREATE`/`REINDEX INDEX CONCURRENTLY` is still building (they are invalid only until the build completes), removing false positives during concurrent builds.
- **`session-settings`**: encodes the full `pg_db_role_setting` precedence (`role+db > role > db > ALTER ROLE ALL > reset_val`), so the reported value matches what a role actually gets on connect.
- **`connection-health`** idle ratio: now advisory — warns at ≥90% idle with no FAIL tier (genuine exhaustion is already covered by `connection-saturation` and `pool-pressure`).
- **`replication-lag`** (logical): WARN/FAIL now require both sustained lag time **and** a material backlog (≥120s + ≥550 MiB to warn; ≥300s + ≥2 GiB to fail), so Debezium's ack cadence alone no longer trips alerts. Physical replication unchanged.

### Fixed

- **`session-settings`**: unit-aware parsing of timeout values like `2000ms`/`1min` that previously crashed and skipped the entire check; `transaction_timeout` (PG17+) is now skipped on older versions instead of reporting a false `MUST be set` failure.
- **`--detail debug`**: renders `Finding.Debug` for single-finding checks (previously only shown for multi-finding checks).

### Removed

- Orphaned `MissingProviderIdTables` generated query (no corresponding check existed).

## [0.2.0] - 2026-04-05

### Added

- **Streaming output**: results print as each check completes instead of batching, with category headers preserved.
- **Per-check timing**: visible with `--detail verbose` or `--detail debug`. Total timing always shown in summary.
- **`SeveritySkip`**: checks that fail to run (timeout, permission error) are reported as `[SKIP]` with the reason, instead of aborting the entire run.
- **`Filter()` function**: public API to filter checks by ID or category before execution.
- **`ReportHandler` type** and **`Collect()` helper**: clean callback-based API for consuming check results.
- **`Options` struct**: replaces long parameter list in `Run()` for better readability and extensibility.
- **`statement_timeout`**: uses PostgreSQL-level timeout per query instead of Go context timeout, keeping the connection healthy after slow queries.
- Extended `InstanceMetadata` with high availability, storage autoscaling, security, and protection fields.
- Standalone CLI binary (`cmd/pgdoctor`).

### Changed

- **Default detail level** changed from `summary` to `brief`.
- **`Run()` API redesign**: accepts `Options` struct with callback, no error return.
- **`SeverityOK.String()`** returns `"pass"` instead of `"ok"` (4-char alignment: pass/warn/fail/skip).
- **`vacuum-settings` check** no longer skips entirely without instance metadata — runs all non-RAM-dependent checks.
- Unified JSON severity output with `Severity.String()` (removed duplicate `severityString` helper).

### Removed

- `Run()` no longer accepts `only`/`ignored` parameters directly — use `Filter()` before calling `Run()`.

## [0.1.0] - 2026-03-10

### Added

- Initial open-source release of pgdoctor.
- 26 PostgreSQL health checks covering configuration, indexes, schema, vacuum, and performance.
- CLI with text and JSON output formats.
- Preset system (`all` and `triage`) for check filtering.
- Shell completion for bash, zsh, fish, and powershell.
- Configurable timeout thresholds for session-settings check.
- Dynamic role discovery for session-settings check.
