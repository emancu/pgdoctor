# Contributing to pgdoctor

Contributions are welcome! This guide covers the basics of adding checks and working with the codebase. For a comprehensive development reference (architecture, patterns, SQL conventions, testing), see [AGENTS.md](AGENTS.md).

## Adding a New Check

### 1. Create the check directory

```bash
mkdir checks/mycheck
```

### 2. Write the SQL query

Create `checks/mycheck/query.sql` with [sqlc](https://sqlc.dev/) annotations:

```sql
-- name: MyQuery :many
SELECT
  schemaname || '.' || relname AS table_name,
  some_metric
FROM pg_stat_user_tables
WHERE some_condition;
```

All queries must be:
- **Read-only** (no writes, no locks)
- **Production-safe** (execute in < 1 second)
- Using `pg_catalog` tables directly (never `information_schema`)

### 3. Write documentation

Create `checks/mycheck/README.md` covering what the check validates, why it matters, and how to fix issues. This is embedded into the binary and shown via `pgdoctor explain`.

### 4. Implement the check

Create `checks/mycheck/check.go`. Each check package exports:
- `Metadata()` returning `check.Metadata`
- `New(queryer)` returning `check.Checker`

See any existing check (e.g., `checks/pgversion/`) for the full pattern.

### 5. Register and generate

Add your check directory to `sqlc.yaml`, then run code generation:

```bash
# Add to sqlc.yaml queries list:
#   - "checks/mycheck"

# Generate database code
sqlc generate

# Register the check in checks.go
go generate ./...
```

### 6. Add tests

Create `checks/mycheck/check_test.go` using table-driven tests with mock query interfaces. See existing checks for the pattern.

### 7. Verify

```bash
go build -o pgdoctor ./cmd/pgdoctor
./pgdoctor list                    # Check appears
./pgdoctor explain my-check        # Documentation renders
go test ./checks/mycheck/...       # Tests pass
go test ./...                      # Full suite passes
```

## Development Setup

pgdoctor requires Go 1.22+ and a PostgreSQL instance for sqlc code generation.

```bash
git clone https://github.com/emancu/pgdoctor.git
cd pgdoctor
go build -o pgdoctor ./cmd/pgdoctor
go test ./...
```

## Severity Philosophy

Choose severity carefully — it determines how users prioritize their work:

- **FAIL**: Requires action. Security risk, data loss potential, or imminent outage.
- **WARN**: Should address. Performance issue, technical debt, or best practice violation.
- **OK**: Passing. Always include at least one OK finding when no issues are detected.

When in doubt, prefer WARN over FAIL. A noisy tool that cries wolf loses trust.

## Code Standards

- **Never edit generated files** (`db/`, `checks.go`)
- **Embed SQL and README** via `//go:embed` directives
- Use `check.NewReport(Metadata())` to create reports
- Access metadata via promoted fields (`report.CheckID`, not local variables)
- Always report `SeverityOK` when no issues are found
- Use table-driven tests with `testify`

## Categories

Checks belong to one of five categories:

| Category | What it covers |
|----------|----------------|
| `configs` | Database configuration, settings, infrastructure health |
| `indexes` | Index health and optimization |
| `vacuum` | Autovacuum, maintenance, and bloat |
| `schema` | Schema design choices and capacity planning |
| `performance` | Runtime performance and query optimization |

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
