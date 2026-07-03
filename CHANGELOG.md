# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **`cache-efficiency`**: now a non-paging advisory — dropped the FAIL tier and lowered the OK threshold to ≥90% (WARN only below 90%). The 90-95% band is dominated by OS-page-cache reads that Postgres counts as `blks_read`, so it was near-constant noise on healthy OLTP instances; genuine memory pressure surfaces in read latency / IOPS, not the global hit ratio.
- **`vacuum-settings`**: the WARN-level memory findings (`work_mem` risky/high-usage and the `maintenance_work_mem` total-budget) now emit a single-line `Details`; the multi-line RAM math moved to `Finding.Debug` (visible at `--detail debug`). The one-liners keep the "baseline worst-case ≈" hedging — `work_mem × max_connections` is simultaneously a floor (each sort/hash node can allocate `work_mem`) and a ceiling (poolers cap effective backends). FAIL variants keep their full blocks.
- **`connection-efficiency`**: `sessions-abandoned` is capped at WARN (previously FAIL above 5%). The metric is a cumulative ratio of already-closed sessions, so it cannot exhaust `max_connections`; real-time exhaustion is `connection-health`'s job. Above 5% the message now reads as chronic client behavior. `sessions-fatal`/`sessions-killed` keep their FAIL tiers.
- **table rows never outrank their finding**: enforced `TableRow.Severity ≤ Finding.Severity` across all checks — red rows under a yellow `[WARN]` header read as contradictory. Critical-tier rows in `table-bloat`, `table-vacuum-health`, `index-bloat`, `toast-storage`, and `uuid-types` are demoted to WARN (partition logic and thresholds unchanged); `replication-lag`'s `replication-state` instead derives its finding severity from the rows, matching its sibling subchecks (its `State` column already carries the tier). A reusable `internal/checktest.AssertSeverityInvariant` guards the invariant in tests.
- **`index-usage`**: recalibrated to WARN-only and its findings now render as tables. `index-cache-ratio` fired on 91/115 production DBs — pure noise — and its FAIL tier was dead code (severity was hardcoded WARN); it now reports only hot, large, well-exercised indexes (≥1,000 scans, ≥100,000 blocks touched, size > 100 MB, hit ratio < 90%), since the metric is confounded by the OS page cache and cumulative-since-stats-reset counters. `unused-indexes`, `low-usage-indexes`, and `index-cache-ratio` now emit `check.Table` output (thresholds for the first two unchanged) instead of capped text blobs.
- **`index-bloat`**: `high-bloat` drops its >70%/>50% split for a single WARN tier — indexes with bloat > 50% **and** size > 1 GB. Qualifying indexes are listed worst-first; smaller or less-bloated indexes trail them, so brief output surfaces the actionable ones and `--verbose` reveals the rest.

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
