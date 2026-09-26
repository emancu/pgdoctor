# pgdoctor

<img src="logo.png" alt="pgdoctor logo" width="180" align="left" />

A command-line tool and Go library for running health checks against PostgreSQL databases.
It identifies misconfigurations, performance issues, and areas for optimization through
read-only checks that are safe to run against production.
It supports PostgreSQL 14 and newer.

<br clear="left" />

## Installation

### Pre-built binaries

Download from [GitHub Releases](https://github.com/emancu/pgdoctor/releases).

### Go install

```bash
go install github.com/emancu/pgdoctor/cmd/pgdoctor@latest
```

### Build from source

```bash
git clone https://github.com/emancu/pgdoctor.git
cd pgdoctor
go build -o pgdoctor ./cmd/pgdoctor
```

## Quick Start

```bash
# List all available checks
pgdoctor list

# Get detailed documentation for a check
pgdoctor explain index-bloat

# Run all checks
pgdoctor run "postgres://user:pass@localhost:5432/mydb"

# Or use an environment variable
export PGDOCTOR_DSN="postgres://user:pass@localhost:5432/mydb"
pgdoctor run

# Run only specific checks
pgdoctor run "postgres://..." --only connection-health,indexes

# Explore all the flags
pgdoctor run "postgres://..." --help
```

## Commands

### `pgdoctor run <DSN>`

Run health checks against a PostgreSQL database. The DSN can be passed as a positional argument or via the `PGDOCTOR_DSN` environment variable.

pgdoctor always uses a connect timeout. When the DSN sets no positive `connect_timeout`, the timeout is 10 seconds. When the DSN sets no `application_name`, pgdoctor uses `pgdoctor`, so you can find its session in `pg_stat_activity`. A value in the DSN wins.

| Flag | Description |
|------|-------------|
| `--only` | Only run these checks or categories |
| `--ignore` | Skip these checks or categories |
| `--preset` | Check preset: `all` (default), `triage` |
| `--detail` | Detail level: `summary`, `brief` (default), `verbose`, `debug` |
| `--output` | Output format: `text` (default), `json` |
| `--hide-passing` | Hide checks and findings that passed |
| `--config` | YAML file with per-check settings, keyed by check ID |

`--only` and `--ignore` accept a check ID with or without its category, so the IDs that `pgdoctor list` prints work as they are: `--only configs/pg-version` is the same as `--only pg-version`.

A preset other than `all` takes precedence over `--only`: pgdoctor ignores `--only` and prints a warning to stderr. `--ignore` still removes checks from the preset. For an unknown preset, pgdoctor prints a warning that names the valid presets and uses `all`.

A config file changes the settings of a check. Each check README lists the keys it reads. A key that is not in the file keeps its default value:

```yaml
session-settings:
  timeout: 5000
```

A setting value can also be a list of scalars. pgdoctor joins the items with commas, so `roles: [app_rw, dba_ro]` is the same as `roles: "app_rw,dba_ro"`.

pgdoctor skips an unknown check ID, a check value that is not a map, and a setting value that is not a scalar or a list of scalars. It prints each skipped entry to stderr at `--detail debug`.

Exit codes are the same for text and JSON output:

| Code | Meaning |
|------|---------|
| `0` | The checks ran. No check reported FAIL. |
| `1` | The checks ran. At least one check reported FAIL. |
| `2` | pgdoctor could not run: connection error, usage error, bad `--config`, unknown flag value, or zero checks selected. |

A usage error is an unknown flag, too many arguments, or an unknown `--only` or `--ignore` value.

### `pgdoctor list`

List all available checks organized by category.

### `pgdoctor explain <check-id>`

Show detailed documentation for a specific check, including what it checks, why it matters, and how to fix issues.

Use `--sql-only` to display just the SQL query used by the check.

### `pgdoctor completion`

Generate shell completion scripts for bash, zsh, fish, or powershell:

```bash
pgdoctor completion zsh > "${fpath[1]}/_pgdoctor"
pgdoctor completion bash > /etc/bash_completion.d/pgdoctor
```

### Global Flags

| Flag | Description |
|------|-------------|
| `--no-color` | Disable colored output |
| `--no-colour` | Alias for `--no-color` |
| `-v`, `--version` | Print version |

## Available Checks

### configs
| Check | Description |
|-------|-------------|
| `pg-version` | PostgreSQL version support status |
| `session-settings` | Role-level timeout and logging configurations |
| `replication-slots` | Replication slot configuration and health |
| `connection-health` | Connection pool saturation, idle ratios, stuck transactions |
| `connection-efficiency` | Session statistics for connection pool efficiency (PG 14+) |
| `temp-usage` | Temporary file creation indicating `work_mem` exhaustion |
| `db-statistics` | Statistics maturity for usage-based analysis |
| `query-stats-capacity` | `pg_stat_statements` entry usage and eviction rate |
| `extension-versions` | Installed extension versions no longer supported upstream |

### indexes
| Check | Description |
|-------|-------------|
| `invalid-indexes` | Indexes in invalid state needing rebuild |
| `duplicate-indexes` | Exact and prefix duplicate indexes |
| `index-usage` | Unused and inefficient indexes |
| `index-bloat` | B-tree index bloat estimates |

### vacuum
| Check | Description |
|-------|-------------|
| `freeze-age` | Transaction ID age approaching wraparound |
| `table-bloat` | Dead tuple percentages indicating vacuum issues |
| `table-vacuum-health` | Per-table autovacuum configuration and activity |
| `vacuum-settings` | Autovacuum, maintenance memory, and vacuum cost settings |

### schema
| Check | Description |
|-------|-------------|
| `pk-types` | Primary keys using bigint or UUID for growth capacity |
| `uuid-types` | UUID columns using native `uuid` type vs varchar/text |
| `sequence-health` | Sequences approaching exhaustion |
| `toast-storage` | TOAST storage usage optimization |
| `partitioning` | Large/transient tables needing partitioning |

### performance
| Check | Description |
|-------|-------------|
| `cache-efficiency` | Buffer cache hit ratio |
| `table-seq-scans` | Tables with excessive sequential scans |
| `partition-usage` | Queries not using partition keys |
| `table-activity` | Table write activity and HOT update efficiency |
| `replication-lag` | Active replication stream lag |
| `uuid-defaults` | UUID columns using v4 random defaults (B-tree bloat) |

## Using as a Library

pgdoctor can be used as a Go library in your own tools:

```go
package main

import (
    "context"
    "fmt"

    "github.com/emancu/pgdoctor"
    "github.com/emancu/pgdoctor/check"
    "github.com/jackc/pgx/v5"
)

func main() {
    ctx := context.Background()
    conn, _ := pgx.Connect(ctx, "postgres://localhost:5432/mydb")
    defer conn.Close(ctx)

    pgdoctor.Run(ctx, conn, pgdoctor.Options{
        Checks: pgdoctor.AllChecks(),
        OnReport: func(report *check.Report) {
            fmt.Printf("[%s] %s\n", report.CheckID, report.Name)
        },
    })
}
```

### Key API

```go
// Run checks with the given options
pgdoctor.Run(ctx, conn, pgdoctor.Options{...})

// Collect reports into a slice (built-in handler)
pgdoctor.Collect(&reports) pgdoctor.ReportHandler

// List all built-in checks
pgdoctor.AllChecks() []check.Package

// Validate filter strings against a check set
pgdoctor.ValidateFilters(checks, filters) (valid, invalid []string)
```

The `db.DBTX` interface matches `pgx.Conn`, so pgdoctor works with any pgx-compatible connection.

## Architecture

Each check is a self-contained Go package under `checks/`:

```
checks/indexbloat/
├── check.go      # Check logic
├── check_test.go # Unit tests
├── query.sql     # SQL query (embedded via go:embed)
└── README.md     # Documentation (embedded via go:embed)
```

Checks implement the `check.Checker` interface:

```go
type Checker interface {
    Metadata() Metadata
    Check(context.Context) (*Report, error)
}
```

All SQL queries are read-only and use PostgreSQL system catalogs (`pg_stat_*`, `pg_catalog`). No data is modified.

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for how to add checks and work with the codebase. For a detailed architecture and conventions reference, see [AGENTS.md](AGENTS.md).

## License

MIT
