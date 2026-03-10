# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-03-10

### Added

- Initial open-source release of pgdoctor.
- 26 PostgreSQL health checks covering configuration, indexes, schema, vacuum, and performance.
- CLI with text and JSON output formats.
- Preset system (`all` and `triage`) for check filtering.
- Shell completion for bash, zsh, fish, and powershell.
- Configurable timeout thresholds for session-settings check.
- Dynamic role discovery for session-settings check.
