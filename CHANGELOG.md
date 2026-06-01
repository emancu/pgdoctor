# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
