# pgdoctor Development Guide for AI Agents

pgdoctor is a PostgreSQL health-check CLI and Go library. This guide helps AI agents contribute effectively.

## Architecture Overview

```
pgdoctor/
├── check/              # Core types (Metadata, Report, Finding, Severity, Checker interface)
├── db/                 # Generated database code (shared by ALL checks via sqlc)
├── checks/             # Individual health checks (self-contained)
│   └── {checkname}/    # Each check is a package with:
│       ├── check.go    #   - Implementation
│       ├── query.sql   #   - SQL queries (sqlc annotations)
│       ├── README.md   #   - Documentation (embedded)
│       └── check_test.go # - Tests
├── checks.go           # Auto-generated: registers all checks (DO NOT EDIT)
├── internal/gen/       # Code generator that produces checks.go
├── internal/cli/       # CLI commands (run, list, explain)
├── cmd/pgdoctor/       # Binary entry point
├── pgdoctor.go         # Library entrypoint: Run(), ValidateFilters(), AllChecks()
└── sqlc.yaml           # sqlc configuration
```

## Core Concepts

### Check Interface

Every check implements `check.Checker`:

```go
type Checker interface {
    Metadata() Metadata
    Check(context.Context) (*Report, error)
}
```

Each check package exports:
- `Metadata()` function returning `check.Metadata`
- `New(queryer)` constructor returning `check.Checker`

### Check Structure

Every check is a self-contained package in `checks/` with:

**Required files:**
- `query.sql` - SQL query with sqlc annotations
- `check.go` - Implements Checker interface with embedded SQL and README
- `README.md` - Documentation (embedded via `//go:embed`)

**Optional files:**
- `check_test.go` - Unit tests

**Generated files (shared):**
All checks share `db/` for sqlc-generated code. Never edit these files.

### Check Implementation Pattern

```go
package mycheck

import (
    "context"
    _ "embed"
    "fmt"

    "github.com/emancu/pgdoctor/check"
    "github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type MyQueryQueries interface {
    MyQuery(context.Context) ([]db.MyQueryRow, error)
}

type checker struct {
    queryer MyQueryQueries
}

func Metadata() check.Metadata {
    return check.Metadata{
        Category:    check.CategoryConfigs,
        CheckID:     "my-check",
        Name:        "My Check",
        Description: "One-line summary",
        Readme:      readme,
        SQL:         querySQL,
    }
}

func New(queryer MyQueryQueries) check.Checker {
    return &checker{queryer: queryer}
}

func (c *checker) Metadata() check.Metadata {
    return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
    report := check.NewReport(Metadata())

    rows, err := c.queryer.MyQuery(ctx)
    if err != nil {
        return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
    }

    if len(rows) == 0 {
        report.AddFinding(check.Finding{
            ID:       report.CheckID,
            Name:     report.Name,
            Severity: check.SeverityOK,
        })
        return report, nil
    }

    report.AddFinding(check.Finding{
        ID:       "subcheck-id",
        Name:     "Subcheck Name",
        Severity: check.SeverityFail,
        Details:  "What's wrong",
    })

    return report, nil
}
```

### Report Structure (Field Promotion)

Report embeds Metadata for direct field access:

```go
type Report struct {
    Metadata // Embedded: CheckID, Name, Category, Description, SQL
    Severity      Severity
    Results       []Finding
}
```

Access metadata via promoted fields:
- `report.CheckID` (not `report.Metadata.CheckID`)
- `report.Name`
- `report.Category`
- `report.SQL`

### Library API

The public API in `pgdoctor.go` accepts an explicit check list, enabling consumers to inject custom checks:

```go
// Run checks against a database connection
pgdoctor.Run(ctx, conn, checks, only, ignored) ([]*check.Report, error)

// Get all built-in checks
pgdoctor.AllChecks() []check.Package

// Validate filter strings against a check set
pgdoctor.ValidateFilters(checks, filters) (valid, invalid []string)
```

### CheckID vs Finding ID

- **CheckID**: Unit of execution. Used for filtering (`--only pg-version`). Same across all findings from one check.
- **Finding ID**: Individual validation within a check. Can differ per finding.

### When to Use Subchecks vs Separate Checks

**Use subchecks (multiple findings in one check) when:**
- Multiple validations analyze the **same query result**
- Validations are interdependent aspects of the same concern
- They logically belong together

**Use separate checks when:**
- Each validation requires a **different query**
- Validations are independent concerns
- Users might want to run them selectively with `--only` / `--ignore`

**Unix philosophy**: Do one thing well. If subchecks don't share the same query result, they should be independent checks.

**Naming principle**: Check names should be **positive** (what we're validating), not negative (what's missing):
- `sequence-health` - "Sequence Health: OK" makes sense
- `pk-types` - "PK Types: OK" makes sense

### Finding Reporting

```go
report.AddFinding(check.Finding{
    ID:       "specific-validation",
    Name:     "Human-readable name",
    Severity: check.SeverityFail,    // OK|Warn|Fail
    Details:  "What's wrong",
    Table:    &check.Table{...},     // Optional structured data
    Debug:    "Debug info",          // Only shown with --detail debug
})
```

### Filtering

Filtering happens at the runner level (`pgdoctor.go`):
- `--only check1,check2` - Only run specified checks
- `--ignore check1,check2` - Skip specified checks
- Checks don't need to implement filtering logic themselves

### Statistics-Dependent Checks

Some checks rely on PostgreSQL runtime statistics (`pg_stat_*` views):

- Use the dedicated `statistics-freshness` check to validate stats maturity
- Add a note in your README indicating the check depends on statistics
- Avoid CROSS JOINs with `pg_stat_database` for stats age

**Checks that depend on statistics:**
- `index-usage` - Uses `pg_stat_user_indexes` for scan counts
- `table-seq-scans` - Uses `pg_stat_user_tables` for scan ratios
- `cache-efficiency` - Uses `pg_stat_database` for cache hit ratios

### Instance Metadata Context

Checks can access instance metadata via context for version detection and instance-aware recommendations:

```go
func (c *checker) Check(ctx context.Context) (*check.Report, error) {
    meta := check.InstanceMetadataFromContext(ctx)

    if meta != nil {
        version := meta.EngineVersion
        vcpus := meta.VCPUCores
        memoryGB := meta.MemoryGB
    }

    // When metadata is nil, use safe defaults
}
```

## SQL Query Conventions

All queries must be production-safe: read-only, no locks, < 1 second execution.

### Preferred Patterns

1. **Use `::regclass` for OID-to-name resolution** - Eliminates JOINs with `pg_class` for simple lookups (30-50% faster)
2. **Use `current_setting()` for config values** - But only for settings that return raw numbers (e.g., `max_connections`). Use `pg_settings.setting` for values with units (e.g., `work_mem`)
3. **Pre-aggregate in CTEs** - Instead of correlated subqueries
4. **Use NOT EXISTS** - Instead of LEFT JOIN + IS NULL
5. **Use `pg_catalog` tables directly** - Never `information_schema` (2-5x faster)
6. **Combine redundant CTEs** - Don't scan the same table multiple times
7. **Explicit JOINs over runtime `::regclass` casts** - In JOIN conditions
8. **Use LATERAL for row-dependent subqueries** - Instead of UNNEST in GROUP BY

### Anti-Patterns

1. Never use `information_schema` views
2. Avoid multiple correlated subqueries
3. Avoid CROSS JOIN for single row lookups
4. Don't scan the same table in multiple CTEs
5. Avoid `::regclass` casting inside JOIN conditions

### Query Checklist

- [ ] Uses `pg_catalog` tables, not `information_schema`
- [ ] Uses `::regclass` for simple OID-to-name resolution (not in JOINs)
- [ ] Pre-aggregates data in CTEs instead of correlated subqueries
- [ ] Avoids unnecessary JOINs
- [ ] Executes in < 1 second on production-scale databases

## Standards and Conventions

### Naming

- **CheckID**: kebab-case (`pg-version`, `invalid-indexes`)
- **Directory**: single word or concatenated (`pgversion`, `invalidindexes`)
- **Finding ID**: kebab-case for subchecks (`index-timestamp`, `single-table`)
- **Consistency**: American English (`indexes` not `indices`)

### Categories

Five categories:
- `check.CategoryConfigs` - Database configuration, settings, infrastructure health
- `check.CategoryIndexes` - Index health and optimization
- `check.CategoryVacuum` - Autovacuum, maintenance, and bloat
- `check.CategorySchema` - Schema design choices and capacity planning
- `check.CategoryPerformance` - Runtime performance and query optimization

### Severity

- `check.SeverityOK` - Check passed, no action needed
- `check.SeverityWarn` - Issue found, non-urgent action
- `check.SeverityFail` - Issue found, urgent action required

Report severity is automatically the maximum across all findings.

## Common Tasks

### Adding a New Check

1. Create `checks/mycheck/` with `query.sql`, `README.md`, `check.go`, `check_test.go`
2. Add `"checks/mycheck"` to `sqlc.yaml` queries list
3. Run `sqlc generate`
4. Run `go generate ./...` (this regenerates both `checks.go` and the `docs/` landing page)
5. Verify: `go build -o pgdoctor ./cmd/pgdoctor && ./pgdoctor list`
6. Commit the updated `docs/` directory (CI will verify it's up to date)

### Modifying Existing Check

**Never edit generated files** (`db/`, `checks.go`).

- Changing SQL: edit `query.sql`, run `sqlc generate`, update `check.go` if signature changed
- Changing logic: edit `check.go` directly
- Changing metadata: edit `Metadata()` function — CLI picks it up automatically

### Debugging Check Discovery

If a check doesn't appear in `list` or `explain`:

1. Run `go generate ./...` to regenerate `checks.go`
2. Verify `Metadata()` is exported and returns `check.Metadata`
3. Verify check directory is in `sqlc.yaml`
4. Run `sqlc generate` after adding

## Critical Rules

### DO

- Embed SQL with `//go:embed query.sql` and include in `Metadata.SQL`
- Embed README with `//go:embed README.md` and include in `Metadata.Readme`
- Use `check.NewReport(Metadata())` to create reports
- Access check info via promoted fields: `report.CheckID`, `report.Name`
- Report `SeverityOK` when no issues found
- Keep checks self-contained
- Use sqlc for all database queries
- Define query interfaces for testability

### DON'T

- Edit generated files (`db/`, `checks.go`)
- Create local `id` or `name` variables (use promoted fields)
- Skip reporting SeverityOK findings
- Hardcode check metadata in CLI
- Create new categories without discussion

## Testing

Standard test pattern using table-driven tests:

```go
func TestMyCheck(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name     string
        data     []db.MyQueryRow
        severity check.Severity
    }{
        {
            name:     "all good",
            data:     []db.MyQueryRow{},
            severity: check.SeverityOK,
        },
        {
            name:     "issue found",
            data:     []db.MyQueryRow{{Value: "bad"}},
            severity: check.SeverityFail,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()

            queryer := &mockQueryer{data: tt.data}
            checker := New(queryer)

            report, err := checker.Check(context.Background())

            require.NoError(t, err)
            assert.Equal(t, tt.severity, report.Severity)
        })
    }
}
```

## File Locations

| What | Where |
|------|-------|
| Check implementation | `checks/{name}/check.go` |
| Check SQL | `checks/{name}/query.sql` |
| Check docs | `checks/{name}/README.md` |
| Generated DB code | `db/` (shared, do not edit) |
| Check registration | `checks.go` (auto-generated, do not edit) |
| Core types | `check/check.go` |
| Library entrypoint | `pgdoctor.go` |
| CLI commands | `internal/cli/` |
| Binary entry | `cmd/pgdoctor/main.go` |
| sqlc config | `sqlc.yaml` |

## Questions to Ask

When adding or modifying checks:
1. What category? (`configs`/`indexes`/`vacuum`/`schema`/`performance`)
2. What CheckID? (kebab-case, unique)
3. What severity for different failure conditions?
4. Multiple findings (subchecks)? If yes, what IDs?
5. What SQL query is needed?
6. What PostgreSQL versions should this support?
